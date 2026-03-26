package main

import (
	"errors"
	"fmt"
	"os"

	"github.com/yusefmosiah/cogent/internal/cli"
)

func main() {
	if err := cli.Execute(); err != nil {
		var exitErr *cli.ExitError
		if errors.As(err, &exitErr) {
			fmt.Fprintln(os.Stderr, exitErr.Error())
			os.Exit(exitErr.Code)
		}

		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
