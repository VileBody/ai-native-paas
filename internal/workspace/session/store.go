package session

import (
	"context"
	"time"
)

type Store interface {
	Connect(context.Context, Session) (Session, error)
	GetSession(context.Context, string) (Session, error)
	ActiveForWorkspace(context.Context, string, string, time.Time, time.Duration) (Session, error)
	Touch(context.Context, string, string, time.Time) (Session, error)
	CloseWorkspace(context.Context, string, time.Time, string) error

	Queue(context.Context, Message) (Message, bool, error)
	GetMessage(context.Context, string) (Message, error)
	ClaimNext(context.Context, string, string, time.Time, time.Time) (Message, error)
	Acknowledge(context.Context, string, string, string, bool, time.Time) (Message, error)
}
