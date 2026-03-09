package core

import "github.com/oklog/ulid/v2"

func GenerateID(prefix string) string {
	return prefix + "_" + ulid.Make().String()
}
