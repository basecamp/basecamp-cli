// Package fakebasecamp is a Basecamp on a loopback port, for testing
// `basecamp connect` end to end: the agent connection ceremony and the
// client_credentials tokens it leads to, the account reads setup and
// admission make, the event feed's two lanes, and the faults a connector has
// to live through.
//
// It is a test double, but not a test file: the connector's own tests, the
// command tests and a real `basecamp connect` subprocess all run against the
// same one, and `go run ./e2e/fakebasecamp` serves it by hand. It takes a
// Reporter rather than a *testing.T, so nothing here imports a test library.
//
// # One world, one lock
//
// Everything the fake knows is a World: the account, its people and
// projects, the recordings in them, the agents' OAuth clients, and the bearer
// tokens it has issued. Every request is answered from it under one mutex,
// and a test changes it through Update, under the same mutex. Nothing is
// answered from a copy, so a change is seen by the next request, whichever
// lane it arrives on.
//
// Requests are authorized as Basecamp authorizes them: a bearer token names a
// person, and a person reads what their projects hold. A project the caller
// is not a member of, and anything in it, answers 404, on the reads and on
// both lanes of the feed.
//
// # The two lanes are published explicitly
//
// Basecamp's poll lane runs about thirty seconds behind its live lane. The
// fake has no clock and no lag: Emit puts an event on the lanes it is told
// to (Live, Poll, or Both), and Publish adds an emitted event to another lane
// later. A test therefore says exactly what each lane serves, which is what
// makes a catch-up, a gap or a duplicate a deterministic scenario rather
// than a race.
//
// A live event is queued to every matching cable subscription under the
// world's lock, so it is ordered against everything else that lock orders: a
// subscription confirmed before Emit receives the event, and one confirmed
// after it does not. The socket writes happen outside the lock, one writer
// per connection, so a client that stops reading stalls only itself.
//
// The poll lane follows the contract eventfeed checks (events always
// present and in ascending id order, an opaque position, and a `next` URL
// that repeats the request's own filters exactly), and the cable follows
// Action Cable as eventfeed speaks it: the actioncable-v1-json subprotocol,
// a welcome, a confirmed subscription, and a ping every three seconds.
//
// # Faults
//
// Inject answers a route with something else (a 429, a 5xx, an
// invalid_client, a different body) for a number of requests or until it is
// removed. Gate holds a route's requests open until it is released. Before
// runs a function as a route is reached, for a change that has to land in
// the middle of a command. DropCable severs the live connections and
// StopPings silences them. Every request is logged, and Await waits, with a
// deadline and never a sleep, for whatever a test needs to have happened.
package fakebasecamp
