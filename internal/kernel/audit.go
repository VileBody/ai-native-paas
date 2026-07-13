package kernel

import (
	"fmt"
	"reflect"
	"strings"

	kernelv1 "github.com/keir-research/ai-native-paas/contracts/kernel/v1"
)

const redactedValue = "[REDACTED]"

var sensitiveKeyFragments = []string{
	"password", "passwd", "secret", "token", "authorization", "credential",
	"api_key", "apikey", "private_key", "privatekey", "access_key", "refresh_key",
}

func RedactMetadata(metadata map[string]any) map[string]any {
	if metadata == nil {
		return nil
	}
	redacted, _ := redactValue(metadata, "").(map[string]any)
	return redacted
}

func redactValue(value any, key string) any {
	if sensitiveKey(key) {
		return redactedValue
	}
	switch typed := value.(type) {
	case map[string]any:
		out := make(map[string]any, len(typed))
		for childKey, childValue := range typed {
			out[childKey] = redactValue(childValue, childKey)
		}
		return out
	case []any:
		out := make([]any, len(typed))
		for i, child := range typed {
			out[i] = redactValue(child, key)
		}
		return out
	case []string:
		return append([]string(nil), typed...)
	case fmt.Stringer:
		if sensitiveKey(key) {
			return redactedValue
		}
		return typed.String()
	default:
		rv := reflect.ValueOf(value)
		if rv.IsValid() && (rv.Kind() == reflect.Map || rv.Kind() == reflect.Slice || rv.Kind() == reflect.Array) {
			// Unknown collection shapes are converted through reflection so a
			// provider-specific map type cannot bypass redaction.
			return redactReflect(rv, key)
		}
		return value
	}
}

func redactReflect(value reflect.Value, key string) any {
	if value.Kind() == reflect.Pointer {
		if value.IsNil() {
			return nil
		}
		return redactReflect(value.Elem(), key)
	}
	switch value.Kind() {
	case reflect.Map:
		out := make(map[string]any, value.Len())
		iterator := value.MapRange()
		for iterator.Next() {
			mapKey := fmt.Sprint(iterator.Key().Interface())
			out[mapKey] = redactValue(iterator.Value().Interface(), mapKey)
		}
		return out
	case reflect.Slice, reflect.Array:
		out := make([]any, value.Len())
		for i := 0; i < value.Len(); i++ {
			out[i] = redactValue(value.Index(i).Interface(), key)
		}
		return out
	default:
		return value.Interface()
	}
}

func sensitiveKey(key string) bool {
	normalized := strings.ToLower(strings.TrimSpace(key))
	normalized = strings.ReplaceAll(normalized, "-", "_")
	for _, fragment := range sensitiveKeyFragments {
		if strings.Contains(normalized, fragment) {
			return true
		}
	}
	return false
}

func cloneAudit(record kernelv1.AuditEnvelope) kernelv1.AuditEnvelope {
	record.Actor.Scopes = append([]string(nil), record.Actor.Scopes...)
	record.Metadata = RedactMetadata(record.Metadata)
	return record
}
