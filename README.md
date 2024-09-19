# contem

[![GoDoc][doc-img]][doc] [![Build][ci-img]][ci] [![GoReport][report-img]][report]

<picture>
  <img src=".github/logo.jpg" width="500" alt="contem logo">
</picture>

</br>

**contem** is a zero-dependency drop-in `context.Context` replacement for graceful shutdown. It is lightweight and easy to use: just `Add` your shutdown methods to the context, call `Wait` to wait an interruption signal and `Shutdown`. **contem** will graceful shutdown and release all added resources with error handling.

Install: `go get github.com/maxbolgarin/contem`


## Benefits of usage

* **Graceful shutdown**: your application will process all incoming requests, close all allocated resources and save data from the buffer before exiting.
* **Ctrl+C support**: you can catch `Ctrl+C` signals and gracefully shutdown the application out of the box without remebering how to use `signal.Notify`.
* **Error handling**: you should handle `defer db.Close()` errors to prevent from an unexpected behaviour. With **contem** you just `AddClose` your closer instead of writing `defer func() {...}`.
* **Less code**: you cannot use `log.Fatal()`, because it calls `os.Exit()` and ignores all defer functions. You should write a shutdown code in every `if err != nil {...}` in the main function. With **contem** you can exit right after a shutdown by using `Exit()` option.
* **Handle file close**: how to close a file when an application stops, if it was opened in the internals of your code? Should you return it right to the main or open at the beginning and propogate throught the app? **contem** allows you to add a `File` to the `Context`, than sync and close it during global shutdown.


## How to become a shutdown master

You can see a full example [here](example/main.go).

```go
// Step 1. Create context and defer Shutdown
var err error
ctx := contem.New(contem.WithLogger(slog.Default()), contem.Exit(&err))
defer ctx.Shutdown()

// Step 2. Create a server and add server's shutdown method to the context
var server *http.Server
srv, err = server.Start(ctx)
if err != nil {
    slog.Error("failed to create server", "error", err)
    return
}
ctx.Add(srv.Shutdown)

// Step 3. Wait for the interruption signal
ctx.Wait()
```


What is going on in this snippet of code?

1. Create a `contem.Context` and defer `Shutdown`:
    * Pass an error to `Exit` to exit with `1` code if there will be errors in future (you should not use `:=` because it will reassign the error variable and it will exit with `0` code)
    * Add `slog` as a logger. It will print `Info` message at the start of shutdown and `Error` message in case of shutdown error. It is useful with `Exit`, because you won't be able to handle error from `Shutdown` in this case.
2. Create a server and add server's shutdown method to the context. You can return from the main without concerns because you have defered `Shutdown` earlier.
3. Wait for the interruption signal. It will block code until `SIGTERM` or `SIGINT` signal is received. After that it will call `Shutdown` and exit the application.


## Contributing

If you'd like to contribute to **contem**, make a fork and submit a pull request. You also can open an issue or text me on Telegram.

Released under the [MIT License]

[MIT License]: LICENSE.txt
[doc-img]: https://pkg.go.dev/badge/github.com/maxbolgarin/contem
[doc]: https://pkg.go.dev/github.com/maxbolgarin/contem
[ci-img]: https://github.com/maxbolgarin/contem/actions/workflows/go.yml/badge.svg
[ci]: https://github.com/maxbolgarin/contem/actions
[report-img]: https://goreportcard.com/badge/github.com/maxbolgarin/contem
[report]: https://goreportcard.com/report/github.com/maxbolgarin/contem
