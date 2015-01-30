// The oplog-tail command is a example implementation of the Go oplog consumer library.
//
// The tail command connects to an oplog agent and prints to the console any operation
// sent thru it. A state-file can be provided to simulate a full replication and mainaining
// the current state.
//
// Some filtering can be performed with "-types" and "-parents" options.
package main

import (
	"flag"
	"fmt"
	"log"
	"os"
	"strings"

	"github.com/dailymotion/oplogc"
)

var (
	password         = flag.String("password", "", "Password to access the oplog.")
	stateFile        = flag.String("state-file", "", "Path to the state file storing the oplog position id (default: no store).")
	types            = flag.String("types", "", "Comma seperated list of types to filter on.")
	parents          = flag.String("parents", "", "Comma seperated list of parents type/id to filter on.")
	allowReplication = flag.Bool("allow-replication", false, "Try to do a full replication (ignored if -state-file is not provided).")
)

func main() {
	flag.Usage = func() {
		fmt.Fprintf(os.Stderr, "Usage of %s:\n", os.Args[0])
		flag.PrintDefaults()
		fmt.Print("  <oplog url>\n")
	}
	flag.Parse()
	if flag.NArg() != 1 {
		flag.Usage()
		os.Exit(2)
	}
	url := flag.Arg(0)

	f := oplogc.Filter{
		Types:   strings.Split(*types, ","),
		Parents: strings.Split(*parents, ","),
	}
	c := oplogc.Subscribe(url, oplogc.Options{
		StateFile:        *stateFile,
		Password:         *password,
		AllowReplication: *allowReplication,
		Filter:           f,
	})

	ops, errs, done := c.Start()
	for {
		select {
		case op := <-ops:
			switch op.Event {
			case "reset":
				fmt.Print("** reset\n")
			case "live":
				fmt.Print("** live\n")
			default:
				if op.Data.Ref == "" {
					fmt.Printf("%s: %s #%s %s/%s (%s)\n",
						op.Data.Timestamp, op.Event, op.ID, op.Data.Type, op.Data.ID, strings.Join(op.Data.Parents, ", "))
				} else {
					fmt.Printf("%s: %s #%s %s (%s)\n",
						op.Data.Timestamp, op.Event, op.ID, op.Data.Ref, strings.Join(op.Data.Parents, ", "))
				}
			}
			op.Done()
		case err := <-errs:
			switch err {
			case oplogc.ErrAccessDenied, oplogc.ErrWritingState:
				c.Stop()
				log.Fatal(err)
			case oplogc.ErrResumeFailed:
				if *stateFile != "" {
					log.Print("Resume failed, forcing full replication")
					c.SetLastId("0")
				} else {
					log.Print(err)
				}
			default:
				log.Print(err)
			}
		case <-done:
			return
		}
	}
}
