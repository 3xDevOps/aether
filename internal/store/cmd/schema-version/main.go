// Command schema-version prints the store schema version this build
// migrates a database to, for release-manifest's -store-schema.
package main

import (
	"fmt"

	"github.com/3xDevOps/Aether/internal/store"
)

func main() { fmt.Println(store.SchemaVersion()) }
