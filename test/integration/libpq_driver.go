//go:build postgres_integration && cgo

package integration_test

/*
#cgo pkg-config: libpq
#include <libpq-fe.h>
#include <stdlib.h>
*/
import "C"

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"sync"
	"time"
	"unsafe"
)

const libpqDriverName = "kernel_libpq"

func init() {
	sql.Register(libpqDriverName, libpqDriver{})
}

type libpqDriver struct{}

func (libpqDriver) Open(dsn string) (driver.Conn, error) {
	value := C.CString(dsn)
	defer C.free(unsafe.Pointer(value))
	connection := C.PQconnectdb(value)
	if connection == nil {
		return nil, errors.New("libpq returned a nil connection")
	}
	if C.PQstatus(connection) != C.CONNECTION_OK {
		err := errors.New(strings.TrimSpace(C.GoString(C.PQerrorMessage(connection))))
		C.PQfinish(connection)
		return nil, err
	}
	return &libpqConn{connection: connection}, nil
}

type libpqConn struct {
	mu         sync.Mutex
	connection *C.PGconn
	closed     bool
}

func (c *libpqConn) Prepare(query string) (driver.Stmt, error) {
	if strings.TrimSpace(query) == "" {
		return nil, errors.New("query is required")
	}
	return &libpqStmt{connection: c, query: query}, nil
}

func (c *libpqConn) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return nil
	}
	c.closed = true
	if c.connection != nil {
		C.PQfinish(c.connection)
		c.connection = nil
	}
	return nil
}

func (c *libpqConn) Begin() (driver.Tx, error) {
	return c.BeginTx(context.Background(), driver.TxOptions{})
}

func (c *libpqConn) BeginTx(ctx context.Context, options driver.TxOptions) (driver.Tx, error) {
	if options.ReadOnly {
		if _, err := c.exec(ctx, "BEGIN READ ONLY", nil); err != nil {
			return nil, err
		}
	} else {
		if _, err := c.exec(ctx, "BEGIN", nil); err != nil {
			return nil, err
		}
	}
	if options.Isolation != driver.IsolationLevel(0) && options.Isolation != driver.IsolationLevel(sql.LevelReadCommitted) {
		level, err := isolationSQL(options.Isolation)
		if err != nil {
			_, _ = c.exec(context.Background(), "ROLLBACK", nil)
			return nil, err
		}
		if _, err := c.exec(ctx, "SET TRANSACTION ISOLATION LEVEL "+level, nil); err != nil {
			_, _ = c.exec(context.Background(), "ROLLBACK", nil)
			return nil, err
		}
	}
	return &libpqTx{connection: c}, nil
}

func isolationSQL(level driver.IsolationLevel) (string, error) {
	switch sql.IsolationLevel(level) {
	case sql.LevelReadUncommitted:
		return "READ UNCOMMITTED", nil
	case sql.LevelReadCommitted:
		return "READ COMMITTED", nil
	case sql.LevelRepeatableRead:
		return "REPEATABLE READ", nil
	case sql.LevelSerializable:
		return "SERIALIZABLE", nil
	default:
		return "", fmt.Errorf("unsupported isolation level %d", level)
	}
}

func (c *libpqConn) Ping(ctx context.Context) error {
	_, err := c.exec(ctx, "SELECT 1", nil)
	return err
}

func (c *libpqConn) ResetSession(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return nil
}

func (c *libpqConn) IsValid() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return !c.closed && c.connection != nil && C.PQstatus(c.connection) == C.CONNECTION_OK
}

func (c *libpqConn) ExecContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	return c.exec(ctx, query, args)
}

func (c *libpqConn) QueryContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	result, err := c.execute(query, args)
	if err != nil {
		return nil, err
	}
	status := C.PQresultStatus(result)
	if status != C.PGRES_TUPLES_OK && status != C.PGRES_SINGLE_TUPLE {
		err := resultError(result)
		C.PQclear(result)
		return nil, err
	}
	columnsCount := int(C.PQnfields(result))
	columns := make([]string, columnsCount)
	oids := make([]uint32, columnsCount)
	for index := 0; index < columnsCount; index++ {
		columns[index] = C.GoString(C.PQfname(result, C.int(index)))
		oids[index] = uint32(C.PQftype(result, C.int(index)))
	}
	return &libpqRows{
		result:  result,
		columns: columns,
		oids:    oids,
		count:   int(C.PQntuples(result)),
	}, nil
}

func (c *libpqConn) exec(ctx context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	result, err := c.execute(query, args)
	if err != nil {
		return nil, err
	}
	defer C.PQclear(result)
	status := C.PQresultStatus(result)
	if status != C.PGRES_COMMAND_OK && status != C.PGRES_TUPLES_OK {
		return nil, resultError(result)
	}
	rowsAffected := int64(0)
	if tuples := C.PQcmdTuples(result); tuples != nil {
		text := C.GoString(tuples)
		if text != "" {
			rowsAffected, _ = strconv.ParseInt(text, 10, 64)
		}
	}
	return libpqResult(rowsAffected), nil
}

func (c *libpqConn) execute(query string, args []driver.NamedValue) (*C.PGresult, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed || c.connection == nil {
		return nil, driver.ErrBadConn
	}
	queryValue := C.CString(query)
	defer C.free(unsafe.Pointer(queryValue))
	if len(args) == 0 {
		result := C.PQexec(c.connection, queryValue)
		if result == nil {
			return nil, errors.New(strings.TrimSpace(C.GoString(C.PQerrorMessage(c.connection))))
		}
		return result, nil
	}
	valuesPointer := C.malloc(C.size_t(len(args)) * C.size_t(unsafe.Sizeof(uintptr(0))))
	if valuesPointer == nil {
		return nil, errors.New("allocate PostgreSQL parameter array")
	}
	defer C.free(valuesPointer)
	values := unsafe.Slice((**C.char)(valuesPointer), len(args))
	allocated := make([]*C.char, 0, len(args))
	for index, argument := range args {
		if argument.Value == nil {
			values[index] = nil
			continue
		}
		encoded, err := encodeDriverValue(argument.Value)
		if err != nil {
			for _, value := range allocated {
				C.free(unsafe.Pointer(value))
			}
			return nil, err
		}
		value := C.CString(encoded)
		values[index] = value
		allocated = append(allocated, value)
	}
	defer func() {
		for _, value := range allocated {
			C.free(unsafe.Pointer(value))
		}
	}()
	result := C.PQexecParams(
		c.connection,
		queryValue,
		C.int(len(args)),
		nil,
		(**C.char)(valuesPointer),
		nil,
		nil,
		0,
	)
	if result == nil {
		return nil, errors.New(strings.TrimSpace(C.GoString(C.PQerrorMessage(c.connection))))
	}
	return result, nil
}

func encodeDriverValue(value any) (string, error) {
	switch typed := value.(type) {
	case string:
		return typed, nil
	case []byte:
		return string(typed), nil
	case int64:
		return strconv.FormatInt(typed, 10), nil
	case float64:
		return strconv.FormatFloat(typed, 'g', -1, 64), nil
	case bool:
		return strconv.FormatBool(typed), nil
	case time.Time:
		return typed.UTC().Format(time.RFC3339Nano), nil
	default:
		return "", fmt.Errorf("unsupported driver value %T", value)
	}
}

type libpqStmt struct {
	connection *libpqConn
	query      string
}

func (s *libpqStmt) Close() error  { return nil }
func (s *libpqStmt) NumInput() int { return -1 }

func (s *libpqStmt) Exec(values []driver.Value) (driver.Result, error) {
	return s.ExecContext(context.Background(), namedValues(values))
}

func (s *libpqStmt) Query(values []driver.Value) (driver.Rows, error) {
	return s.QueryContext(context.Background(), namedValues(values))
}

func (s *libpqStmt) ExecContext(ctx context.Context, args []driver.NamedValue) (driver.Result, error) {
	return s.connection.ExecContext(ctx, s.query, args)
}

func (s *libpqStmt) QueryContext(ctx context.Context, args []driver.NamedValue) (driver.Rows, error) {
	return s.connection.QueryContext(ctx, s.query, args)
}

func namedValues(values []driver.Value) []driver.NamedValue {
	arguments := make([]driver.NamedValue, len(values))
	for index, value := range values {
		arguments[index] = driver.NamedValue{Ordinal: index + 1, Value: value}
	}
	return arguments
}

type libpqTx struct {
	connection *libpqConn
	done       bool
}

func (tx *libpqTx) Commit() error {
	if tx.done {
		return driver.ErrBadConn
	}
	tx.done = true
	_, err := tx.connection.exec(context.Background(), "COMMIT", nil)
	return err
}

func (tx *libpqTx) Rollback() error {
	if tx.done {
		return nil
	}
	tx.done = true
	_, err := tx.connection.exec(context.Background(), "ROLLBACK", nil)
	return err
}

type libpqResult int64

func (libpqResult) LastInsertId() (int64, error) {
	return 0, errors.New("PostgreSQL does not support LastInsertId")
}

func (result libpqResult) RowsAffected() (int64, error) { return int64(result), nil }

type libpqRows struct {
	result  *C.PGresult
	columns []string
	oids    []uint32
	count   int
	index   int
	closed  bool
}

func (rows *libpqRows) Columns() []string { return append([]string(nil), rows.columns...) }

func (rows *libpqRows) Close() error {
	if rows.closed {
		return nil
	}
	rows.closed = true
	if rows.result != nil {
		C.PQclear(rows.result)
		rows.result = nil
	}
	return nil
}

func (rows *libpqRows) Next(destination []driver.Value) error {
	if rows.closed || rows.result == nil || rows.index >= rows.count {
		return io.EOF
	}
	for column := range destination {
		if C.PQgetisnull(rows.result, C.int(rows.index), C.int(column)) != 0 {
			destination[column] = nil
			continue
		}
		value := C.GoString(C.PQgetvalue(rows.result, C.int(rows.index), C.int(column)))
		parsed, err := decodePostgresValue(rows.oids[column], value)
		if err != nil {
			return err
		}
		destination[column] = parsed
	}
	rows.index++
	return nil
}

func decodePostgresValue(oid uint32, value string) (driver.Value, error) {
	switch oid {
	case 16: // bool
		return value == "t" || value == "true", nil
	case 20, 21, 23: // int8, int2, int4
		return strconv.ParseInt(value, 10, 64)
	case 700, 701, 1700: // float4, float8, numeric
		return strconv.ParseFloat(value, 64)
	case 1082: // date
		return time.Parse("2006-01-02", value)
	case 1114, 1184: // timestamp, timestamptz
		return parsePostgresTime(value)
	default:
		return []byte(value), nil
	}
}

func parsePostgresTime(value string) (time.Time, error) {
	layouts := []string{
		"2006-01-02 15:04:05.999999999Z07:00",
		"2006-01-02 15:04:05.999999999Z07",
		"2006-01-02 15:04:05Z07:00",
		"2006-01-02 15:04:05Z07",
		"2006-01-02 15:04:05.999999999",
		"2006-01-02 15:04:05",
		time.RFC3339Nano,
	}
	for _, layout := range layouts {
		if parsed, err := time.Parse(layout, value); err == nil {
			return parsed.UTC(), nil
		}
	}
	return time.Time{}, fmt.Errorf("parse PostgreSQL timestamp %q", value)
}

type postgresError struct {
	state   string
	message string
}

func (e postgresError) Error() string {
	if e.state == "" {
		return e.message
	}
	return e.state + ": " + e.message
}

func resultError(result *C.PGresult) error {
	message := strings.TrimSpace(C.GoString(C.PQresultErrorMessage(result)))
	state := ""
	if value := C.PQresultErrorField(result, C.PG_DIAG_SQLSTATE); value != nil {
		state = C.GoString(value)
	}
	if message == "" {
		message = "PostgreSQL command failed"
	}
	return postgresError{state: state, message: message}
}

var (
	_ driver.Driver           = libpqDriver{}
	_ driver.Conn             = (*libpqConn)(nil)
	_ driver.ConnBeginTx      = (*libpqConn)(nil)
	_ driver.ExecerContext    = (*libpqConn)(nil)
	_ driver.QueryerContext   = (*libpqConn)(nil)
	_ driver.Pinger           = (*libpqConn)(nil)
	_ driver.SessionResetter  = (*libpqConn)(nil)
	_ driver.Validator        = (*libpqConn)(nil)
	_ driver.Stmt             = (*libpqStmt)(nil)
	_ driver.StmtExecContext  = (*libpqStmt)(nil)
	_ driver.StmtQueryContext = (*libpqStmt)(nil)
	_ driver.Tx               = (*libpqTx)(nil)
	_ driver.Result           = libpqResult(0)
	_ driver.Rows             = (*libpqRows)(nil)
)
