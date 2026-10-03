<p align="center">
  <img src="docs/assets/logo-vt-alpha.png" width="220" alt="GoAllAdmin logo">
</p>

<p align="center">
  <strong>Multi-portal auth. Permissions in code. A core you cannot break by accident.</strong><br>
  A small, complete admin scaffold for Go and Vue 3 that is safe to put in production.
</p>

<p align="center">
  <img src="docs/assets/badge-go.svg" height="24" alt="Go 1.26 or later">
  <img src="docs/assets/badge-vue.svg" height="24" alt="Vue 3.5">
  <img src="docs/assets/badge-element-plus.svg" height="24" alt="Element Plus 2.14">
  <img src="docs/assets/badge-mysql.svg" height="24" alt="MySQL 8.0">
  <a href="LICENSE"><img src="docs/assets/badge-license.svg" height="24" alt="License: Apache-2.0"></a>
  <a href="CHANGELOG.md"><img src="docs/assets/badge-version.svg" height="24" alt="Version: v0.2.0"></a>
  <img src="docs/assets/badge-singapore.svg" height="24" alt="Made in Singapore">
</p>

<p align="center">
  <a href="README.md">简体中文</a> · <strong>English</strong>
</p>

<p align="center">
  <a href="https://goalladmin.com">Website</a> ·
  <a href="#built-for-the-age-of-ai-assisted-development">Positioning</a> ·
  <a href="#quick-start">Quick start</a> ·
  <a href="#why-goalladmin">Why</a> ·
  <a href="#documentation">Documentation</a> ·
  <a href="docs/spec.md#12-安全基线">Security</a>
</p>

---

GoAllAdmin is an open-source admin scaffold from STARDATA INTERNATIONAL PTE. LTD. in Singapore, built on Go (Gin, GORM, Casbin) and Vue 3 (Vite, Element Plus). It ships the parts every back office needs and gets wrong most often — multi-portal authentication, permissions and menus declared in code, audited operations, and a framework core that business code cannot reach into — and leaves the rest to you.

This README describes the current source, which provides separate platform, agent and merchant programs and frontends. For a released version, read the documentation at its Git tag; the [changelog](CHANGELOG.md) defines the released scope. Start with the [documentation index](docs/README.md) for usage, deployment, maintenance and development.

## Built for the age of AI-assisted development

GoAllAdmin does not try to be an all-in-one admin framework. We would rather make it a small, secure, clear and extensible foundation for Go back-office systems.

Anyone who has shipped software knows the trade-off: the more features a framework bundles and the more dependencies it drags in, the more code there is to maintain, and the larger the attack surface, the room for misconfiguration and the security risk. In the age of AI-written code the arithmetic is even clearer. Business features are cheap: an AI can generate them to your requirements. What is hard, and what matters, is a core that AI-written code cannot break. So GoAllAdmin keeps authentication, authorization and auditing inside a kernel that business code physically cannot reach; it declares permissions, menus and configuration in code, where an AI can see and change them and where git and code review apply; and it backs every security rule with a reverse test proving that what must not happen does not happen. Features you want are written as your own modules; features you do not want are never in your binary.

Keep the core small. Build what you need. That is GoAllAdmin.

## Why GoAllAdmin?

| Feature | What you get |
|---|---|
| Multi-portal by design | Platform, agent and merchant have separate programs, frontends, user sources, login entries, signing keys, menus and permission sets. Organisation portals isolate accounts and data by organisation. |
| Permissions and menus in code | Modules declare permission codes and menu trees in Go, so they are versioned, reviewable and testable. The database only stores which role holds which codes. Menu management can change titles, icons, order and grouping, but not paths, pages or permission codes; there is no API-management page. Dictionaries work the same way: values declared in code cannot be changed from the admin UI; only labels and colors can. |
| Authentication done properly | 15-minute access tokens plus HttpOnly rotating refresh cookies with family-credential reuse detection; captcha on demand, rate limiting and lockout, with thresholds adjusted within fixed limits in configuration and shown read-only in the admin UI; forced password change; sensitive reads check the database and write transactions recheck permissions and session state. Passwords use Argon2id by default, with bcrypt or FIPS 140-approved PBKDF2 available for deployments that need them. |
| Authorization with boundaries | Route guards (`Public` / `AuthOnly` / `Require`, plus super-only `RequireSuper`); super-administrator and sensitive-permission rules; a non-super user can neither grant nor borrow permissions they do not hold, nor touch a super administrator's account; only a super administrator can reset passwords, and a super administrator's own password can only be reset on the server command line; the last usable super administrator survives concurrent requests. |
| Department-based data scopes | Permission codes decide what someone may do; data scopes decide whose data they may do it to: self, own department, own department and below, or everything, set per role and per resource. Scopes only narrow a permission, never widen it, and business modules apply the same scopes to their own queries with `DataFilter`. |
| Auditable | Operation logging is switched on per route, with password and secret fields masked; every login attempt is recorded; denied access, forged tokens and replayed credentials are recorded as security events, and server failures are grouped into an error log; an investigation timeline reconstructs what a user, IP or session did; logs cannot be edited or deleted and are also written to the log output; sessions can be listed and revoked. |
| A back office out of the box | Data center, security monitoring and workspace; users, roles and grants, departments, positions and menu management; an operations centre (operation logs, login logs, error log, security events, investigation timeline, sessions); settings (dictionaries and read-only security settings); a profile center (details, avatar, password, sign out other devices); server-side lock screen, global search, preferences and dark mode. |
| Organisation portals and access control | Platform administrators create and disable organisations and manage owner accounts. Agent and merchant portals provide data centers, employee accounts, roles, sessions, logs and profiles; owner-only security settings include IP allowlists and denylists with wildcard ranges such as `116.88.8.*`. Agents can view their merchants. |
| Optional applications and multiple instances | Applications are disabled by default and require platform review when enabled; agents can invite merchants. Optional Redis shares counters, captchas and cache invalidation between instances of the same portal, with counter fallback during failures. |
| A core business code cannot touch | The backend `core/internal` is sealed by the Go compiler, the frontend `@ga/shell` restricts deep imports through `exports`, and CI checks dependency direction. Upgrade the framework with `git merge upstream`. |
| Reverse tests | Every security rule has a test proving that what must not happen does not happen, run against a real MySQL. Vitest and Playwright smoke tests cover the frontend. |
| 11 interface languages | English, 简体中文, 繁體中文, 日本語, 한국어, Bahasa Melayu, தமிழ், বাংলা, Русский, Français, Deutsch, including Singapore's four official languages (English, Mandarin Chinese, Malay and Tamil). The language picker shows flags, API error messages follow the interface language, and adding a language only takes locale files. |
| Runs out of the box | Development targets in `make` or plain `go run` / `pnpm` commands; Docker Compose brings up the stack; the new-module guide walks the path from table to page. |

Read the [security baseline](docs/spec.md#12-安全基线) for the trust boundaries, input limits and startup checks.

## Quick start

Requires Go 1.26 or later (use the latest security patch of a supported release), Docker for the local MySQL, and Node 22 or later. Use pnpm; `packageManager` in `web/package.json` pins the project version, currently 10.28.0.

### Run it locally

```bash
git clone https://github.com/goalladmin/goalladmin.git && cd goalladmin

make test-db-up      # 1. local MySQL at 127.0.0.1:3307 with a dev database "goalladmin" (not persisted), plus a Redis for tests at 127.0.0.1:6380
make migrate         # 2. create the tables
make admin           # 3. create super administrator "admin"; the random password is printed once
make run             # 4. backend at http://127.0.0.1:8080 (debug mode)

# in another terminal
make web-install     # 5. frontend dependencies
make web-dev         # 6. platform app at http://localhost:5173 (/api proxied to the backend)
```

Open http://localhost:5173, sign in with the credentials from step 3, change the password when prompted, and the permission-management, operations-centre and settings pages are there.

Create an agent or merchant in the platform portal before running its portal. Run each command below in a separate terminal. Organisation logins also require the organisation code supplied by the platform; initial passwords are shown once and there is no default password.

| Portal | Backend command and address | Frontend command and address |
| --- | --- | --- |
| Platform | `make run`, `http://127.0.0.1:8080` | `make web-dev`, `http://localhost:5173` |
| Agent | `make run-agent`, `http://127.0.0.1:8081` | `make web-dev-agent`, `http://localhost:5174` |
| Merchant | `make run-merchant`, `http://127.0.0.1:8082` | `make web-dev-merchant`, `http://localhost:5175` |

All three backends share a database. On upgrade, run migrations with the new platform program before starting the organisation programs. The test database is not persistent; use a separate persistent database for business deployments. See the [admin guide](docs/guides/admin-guide.md) and [operations guide](docs/guides/operations.md) (Chinese).

### Without make

`make` only saves typing; each target is the commands below plus a few environment variables.

```bash
# Backend (in server/). Run migrations with the platform program first.
cd server
export GA_DB_HOST=127.0.0.1 GA_DB_PORT=3307 GA_DB_NAME=goalladmin GA_DB_USER=ga GA_DB_PASSWORD=ga_test_pw
go run . migrate up
go run . admin create -username admin
go run .                                # start the server; no subcommand means "serve"

# Your own MySQL: create an empty database and a user, then set GA_DB_* accordingly,
# or copy config/config.example.yaml to config.yaml and fill it in.

# Frontend: in another terminal, use the repository's web/ directory.
cd web
pnpm install --frozen-lockfile
pnpm dev              # platform portal at http://localhost:5173
# Agent: pnpm dev:agent; merchant: pnpm dev:merchant
```

Run organisation backends with `GA_SERVER_ADDR=:8081 go run ./cmd/agent` and `GA_SERVER_ADDR=:8082 go run ./cmd/merchant` in `server/`, with the same database environment variables. These programs check migration status but do not run migrations.

In debug mode a temporary JWT secret is generated when `GA_JWT_SECRET_PLATFORM` is unset. Release mode refuses to start without a real one.

### Docker Compose

MySQL, the backend and the frontend served by nginx:

The following is for a new installation, defaulting to MySQL 8.4 LTS. For an existing database volume, pin its current image with `GA_MYSQL_IMAGE` in the existing `.env` and plan the database engine upgrade separately using the [operations guide](docs/guides/operations.md#升级与迁移). Do not overwrite existing configuration or switch an old volume directly.

```bash
cp deploy/.env.example deploy/.env      # fill in the passwords and the JWT secret (openssl rand -base64 48)
docker compose -f deploy/docker-compose.yml up -d
docker compose -f deploy/docker-compose.yml exec server /server admin create -username admin
```

Open http://localhost:8000. The frontend and `/api` must share an origin because the refresh credential is an HttpOnly cookie; `deploy/nginx.conf` is written that way, and the backend's port 8080 is only reachable inside the container network.

The images run in release mode by default, so the refresh cookie is `Secure` (named `__Host-ga_rt_<portal>`): in production put HTTPS in front and set `GA_SERVER_ALLOWED_ORIGINS` in `deploy/.env` to the https address. For a quick local trial in a browser that refuses `Secure` cookies over plain http, temporarily set `GA_SERVER_MODE` to `debug` in `deploy/.env`.

For multiple instances of the platform portal, use the standalone [`docker-compose.multi.yml`](deploy/docker-compose.multi.yml) example (two backends, Redis and nginx). See the [multi-instance deployment guide](docs/guides/multi-instance.md) for startup, upgrades, fallback behavior and availability limits.

To deploy the platform, agent and merchant portals together, use [`docker-compose.portals.yml`](deploy/docker-compose.portals.yml), with three backend images, three frontend builds and separate hostnames. See the [three-portal deployment guide](docs/guides/portals-deployment.md) for secrets, initial account setup and upgrades.

## Writing a business module

Business code lives only in `server/modules/<module>/` and `web/apps/<portal>/`; framework directories stay untouched. A module declares its permission codes and menus in Go and registers itself in `main.go`:

```go
func (m *module) Perms() []rbac.Perm {
	return []rbac.Perm{
		{Code: "order:order:list", Name: "perm.order.order.list", Portal: PortalCode, Group: "order.order"},
		{Code: "order:order:create", Name: "perm.order.order.create", Portal: PortalCode, Group: "order.order"},
	}
}

func (m *module) Routes(r *app.Router) {
	g := r.Portal(PortalCode).Group("/order")
	g.GET("/orders", rbac.Require("order:order:list"), m.h.list)                                   // every route names its guard
	g.POST("/orders", rbac.Require("order:order:create"), m.h.create, oplog.Record("order.create")) // writes are logged
}
```

```go
// server/main.go — the single assembly point; order is initialisation order
func modules() []app.Module {
	return []app.Module{
		system.Module(), // must come first: it registers the platform portal that other modules mount their routes on
		order.Module(),  // your own modules are listed here by hand
	}
}
```

The [new-module guide](docs/guides/new-module.md) walks through the whole path — table, migration, model, repo, service, handler, permission codes, menus, frontend page, messages and tests — using an order module as the running example.

## Layout

```text
server/                 Go backend
  main.go               the single assembly point: load config → app.New → register modules → Run
  core/                 framework core; packages marked [public] are the API surface, the rest is in core/internal/
  modules/system/       users, roles and grants, departments, positions, sessions, profile center, data center, workspace, security monitoring, operations centre (operation, login and error logs, security events, investigation timeline), settings (dictionaries, read-only security settings); registers the platform portal
  modules/agent/        agent management: create agents (each with its owner account), enable/disable, owner, accounts and sessions, agent portal logs
  modules/merchant/     merchant management: likewise, plus moving a merchant to another agent; drop these two from main.go if you don't use the agent and merchant portals
  modules/agentportal/  the agent portal itself and its own back office (sub-accounts, roles, sessions, logs and profile come from core/orgportal in the core, plus a read-only list of the agent's merchants); built only into the agent program cmd/agent
  modules/merchantportal/ the merchant portal itself and its own back office; built only into the merchant program cmd/merchant
  modules/<name>/       your own modules, registered in main.go (for the agent and merchant portals, in the main.go of cmd/agent and cmd/merchant)
  cmd/dev/              make dev: rebuild and restart automatically when the source changes
  migrations/core/      the framework's own SQL migrations
web/                    pnpm workspace
  packages/shell/       @ga/shell: requests, auth, permissions, routing, layout, built-in pages and i18n shared by all three portals
  apps/platform/        the platform portal: one Vite app per portal, the entry is a single createPortalApp call; your own modules' pages go in src/views/<name>/
  apps/agent/           the agent portal (@ga/agent): sign-in by code; sub-account, role and other back-office pages are built into the shell, so this app only has the agent's merchants; if you don't need it, delete this directory and run pnpm install once
  apps/merchant/        the merchant portal (@ga/merchant): likewise; merchant business pages go here
deploy/                 Dockerfiles, docker-compose, nginx example, local MySQL init script
docs/                   technical spec, conventions, API reference, decision log, guides
```

Backend subcommands: `server` (no subcommand means `serve`), `server migrate up|status`, `server admin create -username NAME`, `server admin reset-password -username NAME` (for a super administrator who forgot the password; the admin UI cannot reset a super administrator's password), `server rbac prune`, `server healthcheck`. `make ci` runs every check: gofmt, vet, golangci-lint, dependency direction, the gate self-tests, tidy, third-party license files, backend tests against MySQL, and the frontend lint, typecheck, unit tests and build; `make e2e` runs the Playwright smoke tests against a real backend.

## Configuration

Configuration comes from `server/config/config.yaml` (optional, git-ignored) plus environment variables, with the environment taking precedence. For local development put the database password, JWT secret and port in `config.yaml`; in containers and production provide secrets through the environment: `GA_DB_PASSWORD` and `GA_JWT_SECRET_<PORTAL>`. The full list of keys is at the top of `server/config/config.example.yaml`. The password hashing algorithm needs no configuration (Argon2id by default); to switch to bcrypt or PBKDF2, see the [password hashing guide](docs/guides/password-hash.md) (in Chinese).

Release mode validates on startup: every portal's JWT secret must be at least 32 bytes and not an example value, the database password must be set, `allowedOrigins` must not be empty, `trustedProxies` must not cover every address, and every HTTP timeout must be positive. In release mode the refresh cookie is `Secure`, so production must be served over HTTPS. Behind nginx, put nginx's address in `trustedProxies` — login rate limiting and the IPs in the logs depend on it.

### Logs and auditing

The backend writes logs to standard output only. It does not write log files, and the admin UI does not read them:

- On a single host: `docker compose -f deploy/docker-compose.yml logs -f server`, or `journalctl -u <service>` under systemd.
- To keep and search logs centrally: set `log.format` to `json` (`GA_LOG_FORMAT=json`) and ship them with a collector such as Vector or Fluent Bit to Loki, Elasticsearch / OpenSearch or your cloud provider's log service. Every line carries `request_id`, so the request ID shown in the error log can be searched directly.
- Every login, operation and security event is also written as a separate line at the `AUDIT` level (`audit.login`, `audit.operation`, `audit.security`), regardless of `log.level`. Keeping them outside the database gives you a trustworthy record even if the database is tampered with.

Audit logs in the database have no delete or update endpoints and are never cleaned up automatically. For stricter deployments, give the application its own database account: grant only `INSERT` and `SELECT` on `ga_login_log` and `ga_operation_log`, and `INSERT`, `SELECT` and `UPDATE` (needed to merge counts) on `ga_error_log` and `ga_security_event`, with no `DELETE`; run schema migrations with a separate account (`migrate.auto: false`).

## Documentation

The project is documented in Chinese first; this README is the English edition. Code identifiers, the API and configuration keys are in English.

| Guide | Contents |
|---|---|
| [Documentation index](docs/README.md) | Usage, deployment, maintenance, troubleshooting and development entry points (Chinese) |
| [Admin guide](docs/guides/admin-guide.md) | Three-portal accounts, employee permissions, applications, invitations and IP wildcard rules (Chinese) |
| [Operations guide](docs/guides/operations.md) | Migrations, backups, recovery, logs, account and IP-rule recovery (Chinese) |
| [Troubleshooting](docs/guides/troubleshooting.md) | Access, certificates, login, refresh, permissions, IP rules and Redis (Chinese) |
| [Technical spec](docs/spec.md) | Scope, layout and dependency direction, auth, authorization, tables, API, security baseline, testing, extension and upgrades. "Spec §x.y" in code comments refers to it |
| [API reference](docs/api.md) | Every route, its guard and payload; the list of public routes is kept in sync with the code by a test |
| [Conventions](docs/conventions.md) | Column order for tables, conventions for frontend pages and message keys |
| [Decision log](docs/decisions.md) | The reason behind every departure from the spec or a default |
| [New-module guide](docs/guides/new-module.md) | From table to page, step by step |
| [Password hashing guide](docs/guides/password-hash.md) | Which algorithm to choose, how to set it, what happens to existing passwords when it changes, upgrade and rollback, deployments with FIPS 140 requirements |
| [Multi-instance deployment guide](docs/guides/multi-instance.md) | Two-instance Compose, Redis, proxy affinity, upgrades, failure behavior and process-level smoke tests |
| [Three-portal deployment guide](docs/guides/portals-deployment.md) | Three backend images and frontends, hostname and secret isolation, initial setup and upgrades |
| [Changelog](CHANGELOG.md) | Versions and upgrade notes |
| [AI collaborator rules](CLAUDE.md) | Working rules for AI-assisted development |

## Roadmap

v0.1.0 ships the platform portal, a single instance and MySQL. v0.2.0 adds organisation isolation, three portals, applications and invitations disabled by default, organisation data centers and security settings, optional Redis and deployment examples for multiple instances; Git tags and the changelog define the scope of each release. Later candidates include TOTP, file uploads and OpenAPI. See [`docs/spec.md`](docs/spec.md) §1, §7 and §11.3 for scope and progress.

## Contribution rules

Contributed code must be your own work; where third-party code is genuinely needed, only permissively licensed code (MIT / Apache-2.0 / BSD / ISC) is accepted, with the original attribution and copyright notice kept in the file header. Changes to authentication or authorization must come with tests; the reverse tests listed in `docs/spec.md` §13.2 may not be removed or weakened. Record a decision in `docs/decisions.md` before departing from the spec or a default, and make sure `make ci` is green before committing.

## Authors and maintenance

GoAllAdmin was started in Singapore by Singaporean Chinese developers Danny Xu and Sean Xiang, and is developed and maintained together with the Singapore team of STARDATA INTERNATIONAL PTE. LTD.

## License

GoAllAdmin is released under the [Apache License 2.0](LICENSE). When you use, modify or distribute it, follow the licence and keep `LICENSE`, `NOTICE` and the copyright, patent, trademark and attribution notices in the source; please also keep the copyright line in the page footer.

When you build it yourself, the frontend build, the server binary and the Docker images each come with a generated list of third-party licences, `third-party-licenses.txt`: at the root of the frontend build (next to `index.html`), in `server/bin/` after `make build`, and under `/licenses/` in the server image. Every image also carries this project's own `LICENSE` and `NOTICE` under `/licenses/`.

For commercial support, custom development or other arrangements, contact STARDATA INTERNATIONAL PTE. LTD. through the [website](https://goalladmin.com) or at [dev@stardata.sg](mailto:dev@stardata.sg).

Copyright (c) 2026 STARDATA INTERNATIONAL PTE. LTD. (UEN 201906211G) · Singapore
