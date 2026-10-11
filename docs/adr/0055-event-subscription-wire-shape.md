# ADR-0055: An event frame names its subscription, and subscribing is a command

- Status: Accepted
- Date: 2026-10-11
- Relates to: ADR-0010, ADR-0028; plan m1d D5 and §Changes (step 6); plan m2h §Audit ledger
- Decided by the main session under the owner's standing instruction (apply the recommended
  option), Delegated. It records two choices milestone 1d shipped and wrote only into area
  documents; the m2h audit found them with no ADR.

## Context

Before 1d, `http-websocket.md` §`GET /v1/ws` sent an event as `{"event": <envelope>}`, and
`capability-api.md` §Classification listed `events.subscribe` as a query. A session may hold
several subscriptions, and nothing in that frame said which one an event belongs to. A call
that creates a subscription is not read-only and not idempotent, which the published MCP
annotations would have claimed for a query (`mcp-tool-surface.md` §Annotations).

## Decision

1. An event on the WebSocket is `{"subscriptionId", "event"}`, sent once per subscription it
   matches, and never before the response that gave the client that id has been written on
   the session.
2. `events.subscribe` is a command, global and streaming, neither read-only nor idempotent:
   each call creates a subscription.

## Consequences

- The rules live in `adapters/httpapi/ws.go`, `internal/event/session.go`,
  `http-websocket.md` §`GET /v1/ws` and `capability-api.md` §Classification; the schema
  carries the annotations.
- A client holding two subscriptions to one topic receives the event twice, once per id.

## Alternatives considered

- **`{"event"}` alone**: a client with two subscriptions cannot route an event or tell a
  gap of one from a gap of the other.
- **One frame per event, listing every matching id**: fewer bytes, but a subscription's own
  `seq` and gap could not be told apart, and unsubscribing one would change the others'
  frames.
- **A query that happens to create state**: the annotations a client trusts would be false;
  a retried "query" would leak subscriptions.
