# OpLog Consumer [![godoc](http://img.shields.io/badge/godoc-reference-blue.svg?style=flat)](https://godoc.org/github.com/dailymotion/oplogc) [![license](http://img.shields.io/badge/license-MIT-red.svg?style=flat)](https://raw.githubusercontent.com/dailymotion/oplogc/master/LICENSE) [![build](https://img.shields.io/travis/dailymotion/oplogc.svg?style=flat)](https://travis-ci.org/dailymotion/oplogc)

This repository contains a Go library to connect as a consumer to an [OpLog](https://github.com/dailymotion/oplog) server.

Here is an example of a Go consumer using the provided consumer library:

```go
import (
    "fmt"

    "github.com/dailymotion/oplogc"
)

func main() {
    c := oplogc.Subscribe(myOplogURL, oplogc.Options{})

    ops, errs, done := c.Start()

    for {
        select {
        case op := <-ops:
            // Got the next operation
            switch op.Event {
            case "reset":
                // reset the data store
            case "live":
                // put the service back in production
            default:
                // Do something with the operation
                url := fmt.Sprintf("http://api.domain.com/%s/%s", op.Data.Type, op.Data.ID)
                data := MyAPIGetter(url)
                MyDataSyncer(data)
            }

            // Ack the fact you handled the operation
            op.Done()
        case err := <-errs:
            switch err {
            case oplogc.ErrAccessDenied, oplogc.ErrWritingState:
                c.Stop()
                log.Fatal(err)
            case oplogc.ErrResumeFailed:
                log.Print("Resume failed, forcing full replication")
                c.SetLastId("0")
            default:
                log.Print(err)
            }
        case <-done:
            return
        }
    }
}
```

In case of a connection failure recovery the ack mechanism allows you to handle operations in parallel without loosing track of which operation has been handled.

See `cmd/oplog-tail/` for another usage example.

## Licenses

All source code is licensed under the [MIT License](LICENSE).
