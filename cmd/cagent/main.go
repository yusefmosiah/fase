package main

import (
	"log"

	"github.com/yusefmosiah/cagent/internal/cli"
)

func main() {
	if err := cli.Execute(); err != nil {
		log.Fatal(err)
	}
}
