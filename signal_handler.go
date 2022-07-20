package mongotest

import (
    "fmt"
    "os"
    "os/signal"
    "syscall"
)

func init() {
    // Spawn a signal handler listening in the background for kills
    go spawnSignalListener()
}

// spawnSignalListener listens for kill signals made to the program and attempts
// to shut down all running containers
func spawnSignalListener() {
    killSignal := make(chan os.Signal, 1)
    // Register a handler to listen for SIGTERM/SIGINT. Once the signal is
    // received, then it will forward that signal to the channel
    signal.Notify(killSignal, syscall.SIGTERM, syscall.SIGINT, syscall.SIGABRT, syscall.SIGKILL, syscall.SIGQUIT, os.Interrupt)
    // Block and wait to receive an OS signal
    // This thread will happily park itself for the life of a CF program
    // and wait.
    <-killSignal
    ReapRunningContainers()
}

func ReapRunningContainers() {
    cachedConnections := getAllCachedConnections()
    for _, testConn := range cachedConnections {
        if testConn == nil { continue }
        fmt.Println("Killing container from ReapRunningContainers")
        _ = testConn.KillMongoContainer()
    }
}
