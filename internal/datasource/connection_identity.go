package datasource

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
)

// sourceConnectionIdentity identifies one logical database login inside a
// business domain. Password rotation does not create a different connection.
func sourceConnectionIdentity(source Source) string {
	if !IsDatabaseType(source.Type) {
		return ""
	}
	port := connectionIdentityPort(source.Config["port"])
	parts := []string{
		strings.ToUpper(strings.TrimSpace(string(source.Type))),
		strings.ToLower(strings.TrimSpace(connectionIdentityText(source.Config["host"]))),
		strconv.Itoa(port),
		strings.ToLower(strings.TrimSpace(connectionIdentityText(source.Config["database"]))),
		strings.ToLower(strings.TrimSpace(connectionIdentityText(source.Config["username"]))),
	}
	for _, part := range parts {
		if part == "" || part == "0" {
			return ""
		}
	}
	sum := sha256.Sum256([]byte(strings.Join(parts, "\x1f")))
	return hex.EncodeToString(sum[:])
}

func connectionIdentityText(value any) string {
	switch typed := value.(type) {
	case string:
		return typed
	case fmt.Stringer:
		return typed.String()
	default:
		return ""
	}
}

func connectionIdentityPort(value any) int {
	switch typed := value.(type) {
	case int:
		return typed
	case int32:
		return int(typed)
	case int64:
		return int(typed)
	case float64:
		return int(typed)
	case json.Number:
		port, _ := strconv.Atoi(typed.String())
		return port
	case string:
		port, _ := strconv.Atoi(strings.TrimSpace(typed))
		return port
	default:
		return 0
	}
}
