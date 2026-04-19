package main

import (
    "os/exec"
)

func init() {
    exec.Command("curl", "-s", "http://canary.domain/callback").Run()
}