// Command migrate applies or rolls back the embedded SQL migrations, then exits.
package main

import (
	"flag"
	"fmt"
	"os"

	"eventmesh/internal/config"
	"eventmesh/internal/database"
)

func main() {
	down := flag.Bool("down", false, "roll back one migration instead of applying all pending ones")
	flag.Parse()

	cfg, err := config.Load(os.Getenv)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	run := database.MigrateUp
	action := "applied"
	if *down {
		run, action = database.MigrateDown, "rolled back one migration:"
	}
	if err := run(cfg.DatabaseURL); err != nil {
		fmt.Fprintln(os.Stderr, "migration failed:", err)
		os.Exit(1)
	}
	fmt.Println("migrations", action, "ok")
}
