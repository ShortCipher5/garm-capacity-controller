# Read-only web demo

This is a React UI with an optional Go fixture API. Both use identical static synthetic data. No host, pool, runner or credential can be changed. The history chart is sample allocated runner counts, **not** real utilization telemetry. Labels and priority are examples, not an implemented scheduler.

## Frontend preview

Requires Node 24 and npm. From the repository root:

```sh
npm --prefix web ci --ignore-scripts
npm --prefix web test
npm --prefix web run dev
```

Open `http://127.0.0.1:5173/?preview=1`. This mode does not need a backend or token. No external fonts, analytics or CDN scripts are used. The development server is not a production deployment.

## Go API and built web app

```sh
npm --prefix web run build
# In a dedicated shell, create a NEW disposable token. Never reuse an API key.
export CAPACITY_DEMO_TOKEN="$(openssl rand -hex 32)"
go run ./cmd/capacity-demo -listen 127.0.0.1:8080 -assets web/dist
```

Open `http://127.0.0.1:8080`, and enter that disposable token. The API accepts it only in the Authorization header, never a query string. The browser does not save it to localStorage or a cookie. Refreshing or disconnecting returns to the connection screen. Cancel discards an in-flight request; a late response cannot reopen it.

The server refuses non-loopback listen addresses and Host headers, does not enable CORS, has no write routes and never logs request credentials. Static assets are public **locally**, including the same synthetic fixture used by the preview. The API token is a demo-only gate, not a production authentication system. It protects no production data and has no role management, rotation workflow or rate limiting. Production OIDC, authorization, secret storage and audit are still required before real integrations.

The API and the reservation simulator are intentionally separate in this slice. The UI must not imply its fixture is the simulator's live ledger, that a sample host's drain status affects anything, or that data shown proves live enforcement.

## Verification

`npm test` checks fixture accounting, validation and parity with the Go API. `npm run test:ui` runs Playwright smoke tests for desktop/mobile, preview exit, Back/Forward, repeated connect/disconnect, 401 responses and late-response cancellation. Install Chromium for this suite with `npx playwright install chromium`. CI uploads synthetic dashboard screenshots, never credentials. Go tests cover the API's read-only routes, token requirements, loopback Host checks, security headers and missing assets.
