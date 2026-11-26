# goproxy roadmap

Last updated: 2026-11-26

This roadmap replaces the original design spec and implementation plan. It
records what goproxy does today, the decisions that still hold, the gaps found
in the current code, and what comes next, in the planned order.

## Where the project stands

Stage 1, the forward proxy, is done and verified:

- **Plain HTTP forwarding.** Requests are rewritten from absolute form to
  origin form, hop-by-hop headers are removed in both directions, and the
  client connection stays open between requests (keep-alive). Interim `1xx`
  responses, `HEAD` and `Expect: 100-continue` are handled.
- **HTTPS through `CONNECT` tunnels.** Bytes are copied both ways, each side is
  half-closed (`CloseWrite`) when the other finishes, and TLS bytes already in
  the buffer are kept.
- **Errors.** `400` for malformed or non-absolute requests, `502` for
  unreachable targets, `504` for dial timeouts.
- **Logging.** One line per request or tunnel. `-v` dumps the headers before
  and after each hop.
- **Shutdown.** Exits cleanly on `SIGINT` and `SIGTERM`.
- **Tests.** 38 tests against local servers only (no internet). They pass with
  `-race`, and `go vet` and `gofmt` are clean.
- **Real use.** Tested with curl, a Windows browser, an iPhone on the LAN
  (`-addr 0.0.0.0:8080`), and as a systemd service on an Ubuntu 24.04 server.

## Principles that still apply

These come from the original design. Every item below has to respect them.

1. **Learning first.** The goal is to see how a proxy works on the inside. A
   feature is judged by how much of the mechanism it shows, not just by whether
   it works.
2. **Standard library only.** No third-party modules.
3. **Nothing that hides the mechanism.** The proxy never uses `http.Server`,
   `http.Transport`, `http.Client` or `httputil`. From `net/http` it uses only
   `ReadRequest`, `ReadResponse`, `StatusText`, the `Request`, `Response` and
   `Header` types, and their `Write` methods. Tests may use `httptest` and
   `http.Client`. Phase 4 adds `crypto/tls` and `crypto/x509`, which are part
   of the standard library and are the mechanism being learned there.
4. **Readable over clever.** Comments explain why, not what.
5. **Safe by default.** The proxy listens on `127.0.0.1` unless told otherwise.
   Any feature that widens access is opt-in.
6. **Every behavior has a test.** The tests run against local servers.

## Known gaps in the current code

These came up while reviewing the code for this roadmap. Each one is covered
by a phase below.

| Gap | Where | Phase |
|-----|-------|-------|
| Doc comments still point to the deleted plan ("Tarea 3, paso 5"), and some read as step-by-step instructions instead of describing what the function does. | `conn.go`, `forward.go`, `tunnel.go` | 0 |
| The error from writing `200 Connection Established` is ignored. Some status codes are literals (`400`) instead of `http.StatusBadRequest`. | `tunnel.go:34,44,65`, `conn.go:68` | 0 |
| No size limit on the request line or headers. `http.ReadRequest` reads them without a cap, and the 60 s idle deadline limits time, not bytes. | `conn.go:91` | 1 |
| No deadline on the upstream connection after dialing. A target that accepts the connection and never answers leaves the goroutine and the client waiting forever. Writes to a client that stops reading have no deadline either. | `forward.go:103` | 1 |
| No limit on concurrent connections. | `conn.go:33` | 1 |
| No authentication. `CONNECT` and forwarding accept any port and any destination, including loopback, private ranges and `169.254.169.254` (cloud metadata). That's fine on `127.0.0.1`, but risky on `0.0.0.0`, as in the iPhone and server runs. | `forward.go`, `tunnel.go` | 1 |
| Log lines don't say which client made a request. `-v` header blocks from concurrent requests can interleave, because each `Headers` call takes the lock on its own. | `logging.go:61` | 2 |
| Shutdown only closes the listener. Open tunnels are cut when the process exits, including on `systemctl restart`. | `main.go` | 2 |
| Timeouts are hard-coded. | `proxy.go:28` | 2 |
| No `Via` header. RFC 9110 §7.6.3 requires proxies to add one. | `forward.go` | 3 |

## Order

Housekeeping comes first because removing the plan and spec leaves dangling
references. Safety comes next because goproxy has already run on open
addresses. Reading HTTPS (Phase 4) was the planned second stage. It comes after
Phases 1 to 3 because MITM makes the proxy more powerful, and it's easier and
safer to build on top of authentication, limits and better logs. Phases 2 and 3
can be swapped or mixed.

Stage 1 was built in a mixed mode: Claude wrote the scaffolding and the tests
first, and the user wrote the connection loop, the forwarding and the tunnel.
The same split works for each item here. Each item gets its own branch, with
its tests written before the code. When an item is done, tick it here and
update the README.

## Phase 0: Housekeeping

- [ ] Remove the `Tarea N, paso M` lines. Rewrite the doc comments of `Serve`,
  `handleConn`, `readRequest`, `writeError`, `removeHopByHop`, `forward` and
  `tunnel` so they say what each function does and why, not the steps to
  write it.
- [x] Change the README's *Design documents* section to link to this roadmap
  instead of the deleted spec and plan.
- [ ] Use `http.Status*` constants everywhere. Handle the error from writing
  `200 Connection Established`: log it and end the tunnel instead of copying
  over a broken connection.
- [ ] Add CI with GitHub Actions: `go test -race ./...`, `go vet ./...` and
  `gofmt -l .` on every push and pull request.

**Done when** `grep -n Tarea *.go` prints nothing, the README has no broken
links, and CI is green.

## Phase 1: Safe to expose

With no authentication, anyone who can reach the port can use the proxy, and
through it they can reach whatever the host can reach. This phase makes it safe
to run on a LAN or a server.

- [ ] **Proxy authentication.** Add `-auth user:password`, and later a users
  file. A request without a valid `Proxy-Authorization: Basic …` gets
  `407 Proxy Authentication Required` with
  `Proxy-Authenticate: Basic realm="goproxy"`. Compare credentials with
  `crypto/subtle.ConstantTimeCompare`. Apply it to both forwarding and
  `CONNECT`.
  - *Teaches:* why `Proxy-Authorization` is hop-by-hop (it's already
    stripped), `407` versus `401`, and that Basic auth travels in cleartext
    between the client and the proxy.
  - *Tests:* no header gives `407`, a wrong password gives `407`, the right
    one is forwarded and the header never reaches the target. The same for
    `CONNECT`.
- [ ] **Client allowlist.** With `-allow 192.168.40.0/24,127.0.0.1/32`,
  connections from any other address are closed right after `Accept`.
  - *Teaches:* `net.ParseCIDR` and `RemoteAddr`.
- [ ] **Refuse to run open by accident.** On a non-loopback address, refuse to
  start without `-auth` or `-allow`, unless `-open` is given explicitly.
- [ ] **Destination policy.**
  - `CONNECT` only to port 443 by default (`-connect-ports 443,8443`). Other
    ports get `403`. This stops the tunnel being used for things like SMTP
    spam.
  - Refuse loopback, private (RFC 1918), link-local (including
    `169.254.169.254`) and unspecified addresses, unless `-allow-private` is
    given. Check the resolved IP, not the host name, and dial that same IP,
    so the DNS answer can't change between the check and the dial (DNS
    rebinding). `net.Dialer.Control` sees the final address.
  - *Teaches:* SSRF, and why a proxy on a cloud server is a door to its
    metadata service.
- [ ] **Limits.**
  - Cap the request line plus headers (for example, 64 KB) by counting the
    bytes read while parsing and resetting the count for each request. Over
    the limit, reply `431 Request Header Fields Too Large`.
  - Add a response-header timeout toward the target (for example, 30 s): a
    read deadline on the upstream connection until `ReadResponse` returns.
    If it expires, reply `504`.
  - Add a write deadline toward the client, renewed while data flows.
  - Cap concurrent connections with `-max-conns`, using a semaphore channel.
    When it's full, reply `503`.
  - Add an optional idle timeout for tunnels with `-tunnel-idle`, off by
    default. It resets on every read in either direction.
  - *Tests:* trigger each limit with a local fake client or fake server.

**Done when** the proxy can run on `0.0.0.0` with `-auth` and none of the gaps
marked Phase 1 can be reproduced.

## Phase 2: Observability and operations

- [ ] **Who and which.** Add the client address to every log line, and a short
  connection ID (`#12`) shared by a request's log line, its `-v` header blocks
  and its tunnel lines. Write a request's four `-v` blocks in one locked write,
  so they don't interleave with other requests.
- [ ] **Log format.** Keep the current human-readable format as the default.
  Add `-log json` with `log/slog` for servers, where journald, `grep` and `jq`
  read the output. JSON lines carry the full date.
- [ ] **Stats.** Count active connections, open tunnels, requests by status and
  bytes each way with `sync/atomic`. Print them on `SIGUSR1`. Also serve them
  as a small status page, written by hand, when a request arrives in origin
  form for `/` (today that gets a `400`).
- [ ] **Graceful shutdown.** On `SIGINT` or `SIGTERM`, stop accepting, let
  in-flight HTTP requests finish, give tunnels a grace period
  (`-shutdown-timeout 10s`), then close whatever is left. A second `Ctrl+C`
  exits immediately.
  - *Teaches:* tracking connections with `sync.WaitGroup`, and cancellation
    with `context`.
- [ ] **Configuration.** Add flags for the dial, idle and response-header
  timeouts. Once flags multiply (users, allowlists, policies), add a JSON
  config file (`-config goproxy.json`, read with `encoding/json`). Flags
  override the file.
- [ ] **Deployment files.** Commit the systemd unit used on the server as
  `deploy/goproxy.service`, with hardening (`DynamicUser=yes`,
  `NoNewPrivileges=yes`, `ProtectSystem=strict`, `Restart=on-failure`), plus a
  short `docs/deploy.md`. Add a `-version` flag that reads
  `debug.ReadBuildInfo`.

**Done when** you can tell from the log who made each request, and
`systemctl restart goproxy` doesn't cut short downloads that finish within the
grace period.

## Phase 3: HTTP correctness

- [ ] **`Via` header.** Add `Via: 1.1 goproxy` to forwarded requests and
  responses, appended to any existing value. Use it to detect loops: a request
  that already carries goproxy's token gets `508 Loop Detected`.
- [ ] **Reuse connections to targets.** Keep idle connections per `host:port`
  in a small pool (max idle per host, idle timeout) instead of opening one per
  request. Today every request asks the target to close (`req.Close = true`).
  - *Teaches:* why `Content-Length` and `chunked` decide whether a connection
    can be reused, stale connections, retrying only idempotent requests, and
    what `http.Transport` does behind the scenes.
  - Measure before and after with `testing.B` benchmarks.
- [ ] **WebSocket over plain HTTP (`ws://`).** Today `Upgrade` and `Connection`
  are stripped, so an upgrade can never happen. Pass `Upgrade` through when the
  client asks for it, and on `101 Switching Protocols` switch to the tunnel's
  byte copy.
  - *Teaches:* the one case where hop-by-hop headers have to be put back on
    purpose. The response loop already treats `101` specially.
- [ ] **PAC file.** Serve `/proxy.pac` (`application/x-ns-proxy-autoconfig`)
  from the proxy, so phones and Windows can use automatic proxy setup with a
  single URL.

**Done when** the target sees `Via`, a benchmark shows the effect of the pool,
and a `ws://` echo test passes through the proxy.

## Phase 4: Reading HTTPS (MITM)

This is the second stage planned in the original design. It's off unless
`-mitm` is given.

- [ ] **Local certificate authority.** `-ca-dir` generates a CA key and
  certificate on first use with `crypto/x509` and `crypto/ecdsa`, stored with
  `0600` permissions. Document how to trust it, and how to remove it, on
  Windows, Firefox, iOS (*Settings → General → About → Certificate Trust
  Settings*) and Linux.
- [ ] **Per-host certificates on the fly.** For each `CONNECT host:443`, sign a
  leaf certificate with `host` in its SAN, and cache it in memory until it
  expires.
  - *Teaches:* certificate chains, SANs, and why the browser trusts the leaf
    only after you install the CA.
- [ ] **End and restart TLS.** After `200 Connection Established`, run
  `tls.Server` toward the client (choosing the certificate in
  `GetCertificate`) and `tls.Client` toward the target, which still checks the
  real certificate. Offer only `http/1.1` in ALPN, so the traffic inside is
  HTTP/1.1. Then read requests in a loop and reuse the forwarding logic. That
  means refactoring `forward` so the upstream connection and the scheme are
  passed in instead of dialed inside it.
- [ ] **Log and dump HTTPS.** Log lines show `GET https://host/path 200`
  instead of an opaque tunnel, and `-v` dumps headers as it does for plain
  HTTP. An optional `-dump-bodies` prints bodies up to a size cap.
- [ ] **Bypass list.** Apps that pin certificates, such as many Apple apps like
  the ones seen in the iPhone traffic, fail under MITM. `-mitm-skip
  apple.com,*.icloud.com` keeps those hosts as plain tunnels. When a client's
  TLS handshake fails, the log says so clearly.
- [ ] **Tests.** A local TLS target from `httptest.NewTLSServer`, a test client
  that trusts the generated CA, certificate caching, the bypass list, and an
  invalid upstream certificate giving `502`.

**Done when** a browser with the CA installed shows HTTPS requests in the log,
and the same browser warns once the CA is removed.

## Phase 5: Further learning projects

These are independent of each other. Pick them in any order.

- [ ] **Hand-written HTTP/1.1 parser.** This is approach C from the original
  design. Replace `readRequest` (`conn.go:91`), the single seam built for this:
  request line, headers with limits, `Content-Length` and `chunked`. Add fuzz
  tests (`testing.F`) that compare it with `http.ReadRequest`.
  - *Teaches:* request smuggling (conflicting `Content-Length` and
    `Transfer-Encoding`), and why a parser is a security boundary.
- [ ] **SOCKS5** (RFC 1928). Detect it by the first byte (`0x05`) on the same
  listener, or use a second one. Support no-auth and username/password
  (RFC 1929), the `CONNECT` command, and IPv4, IPv6 and domain addresses. It
  reuses the tunnel's byte copy.
- [ ] **Filtering.** A blocklist file of hosts or suffixes (ads, trackers).
  Blocked requests and blocked `CONNECT`s get `403`. Reload the list on
  `SIGHUP`.
- [ ] **Caching** for HTTP (RFC 9111). Cache `GET` responses that allow it
  (`Cache-Control: max-age`, `Expires`), revalidate with `ETag` and
  `If-None-Match`, and keep everything in memory with a size cap. This only
  makes sense for plain HTTP, or for HTTPS after Phase 4.
- [ ] **Chaining to another proxy.** With `-upstream http://host:port`, send
  requests in absolute form with `WriteProxy` (the other half of the comment
  in `forward.go`), and send `CONNECT` on to the next proxy.
- [ ] **HTTP/2 toward clients after MITM.** Offer `h2` in ALPN. This is a large
  item: frames, streams and HPACK all have to be written by hand, because the
  standard library doesn't export them.

## Out of scope

Still not planned:

- Production-grade performance or high availability.
- `http.Server`, `http.Transport`, `http.Client`, `httputil.ReverseProxy` or
  third-party proxy libraries inside the proxy.
- HTTP/3 (QUIC).
- Running as an open proxy. Any mode that accepts connections from other
  machines requires authentication or an allowlist (Phase 1).
- Reverse proxy features.
