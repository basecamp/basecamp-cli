package setup

import "errors"

// ErrDangerousShared is dangerous mode asked for while other people can give
// the agent work.
var ErrDangerousShared = errors.New("dangerous mode is only for an agent that you alone can give work to")

// ErrDangerousWhileShared is letting other people give the agent work while
// dangerous mode is on.
var ErrDangerousWhileShared = errors.New("other people can't be allowed to give your agent work while dangerous mode is on")

// DangerousRisk is what dangerous mode lets the agent do, for a person
// deciding whether to turn it on.
const DangerousRisk = `Dangerous mode lets your agent run any command on this computer, as you:
tests, linters, git, installing things, deleting things. It asks nobody
first.

It reads the whole thread it's working in, including other people's
comments and attachments, and text in any of them could steer it into
running something. It can reach everything you can: your files, your SSH
keys, your Basecamp login, your cloud tokens, and the network. Only turn it
on for projects where you trust everything that gets posted.`

// DangerousSharedExplainer is why dangerous mode and letting other people
// give the agent work don't go together yet.
const DangerousSharedExplainer = `Why these don't go together yet:

  - Anyone who can give your agent work could ask it to run anything on
    this computer, as you: read your files and keys, change or delete
    them, or send them somewhere.
  - With dangerous mode, a request is a command on your machine. Keeping
    the agent to you alone means the only person who can make those
    requests is you.
  - It isn't turned on for shared agents until the agent's work can run
    in a box that holds only the project, with nothing of yours in it to
    reach.`
