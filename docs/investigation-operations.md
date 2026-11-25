# Investigation Database Operations

## Provision PostgreSQL on Render

1. In the Render dashboard, find the production Watchdog service and check its workspace and region. Run a single Watchdog instance as described in [ADR 0001](adr/0001-durable-historical-investigation-storage.md#deployment-decision).
2. In the same workspace and region, create a managed PostgreSQL 17 database on a paid compute plan that includes point-in-time recovery. Wait for the database to become available.
3. In the database's **Connect** menu, copy the **Internal Database URL**. Set `sslmode=require` by adding `?sslmode=require` to a URL with no query string or `&sslmode=require` to an existing query string. Replace any existing `sslmode` value with `require`.
4. Save the URL as the `DATABASE_URL` secret in the Watchdog service's **Environment** settings. Set the API token before deploying. Watchdog connects and runs pending migrations at startup.

[Render Postgres setup](https://render.com/docs/postgresql-creating-connecting) · [Render environment settings](https://render.com/docs/configure-environment-variables)

## Configure the API token

1. Generate a token from 32 random bytes in a private terminal.

   ```sh
   openssl rand -base64 32
   ```

2. Set `INVESTIGATION_API_TOKEN` in the Watchdog service's **Environment** settings. Store the same value in the client application's secret configuration. The client sends it in the `Authorization: Bearer <token>` header. Keep the token and connection URL out of source control, logs, and issue comments.
3. Choose **Save and deploy** if the existing Watchdog build includes investigation database startup and API token authentication. If it needs a newer build, choose **Save, rebuild, and deploy** with a revision that includes both features. After deploying, run the [verification checks](#verify-a-deploy).

## Rotate the API token

1. Generate a new token using the same command and set it as `INVESTIGATION_API_TOKEN_NEXT`. Keep the current `INVESTIGATION_API_TOKEN` in place. Save and deploy, then verify both tokens return `200` from `GET /v1/servers`.
2. Update the client's secret configuration to use the new token. Verify the client can still read from the API.
3. Set `INVESTIGATION_API_TOKEN` to the new value and remove `INVESTIGATION_API_TOKEN_NEXT` in the same change. Save and deploy, then verify the new token returns `200` and the old token returns `401`.

## Back up and restore

Render continuously backs up paid PostgreSQL instances for point-in-time recovery. The available recovery window depends on the workspace plan. Under **Recovery** in the Render dashboard, confirm the available window before choosing a restore point.

1. In the original database's **Recovery** page, choose **Restore Database**. Select a timestamp before the data loss or corruption and restore into a new instance in the same workspace and region.
2. Wait for the replacement to become available. Connect to it and check that the expected investigation tables and records are present before switching Watchdog over.
3. Replace `DATABASE_URL` on Watchdog with the replacement's internal URL and set `sslmode=require`. Save and deploy, then run the [verification checks](#verify-a-deploy) and confirm the expected history is readable. Keep the original instance until verification passes.

Any data lost from PostgreSQL is investigation history. Feed collection and Prometheus metrics continue independently. [Render recovery and backups](https://render.com/docs/postgresql-backups)

## Recover from a database outage

If PostgreSQL is unavailable at startup, Watchdog logs `Investigation database unavailable; continuing without investigation storage` and continues collecting feeds and serving `/metrics`. Database-backed investigation API requests made with a valid token return `503` while storage is disabled.

Check the database status and the `DATABASE_URL` setting. Once PostgreSQL is reachable, restart Watchdog to re-enable recording and database-backed API reads. Run the [verification checks](#verify-a-deploy) after the restart.

## History retention

The investigation history policy keeps closed episodes and their evidence for 90 days after closure. Open episodes for configured servers are kept until they close. Open episodes for removed servers expire 90 days after their last observation. Automatic cleanup is tracked in [#203](https://github.com/OneBusAway/watchdog/issues/203).

## Verify a deploy

- Check the Watchdog startup logs for `Investigation database connected and migrated`.
- Verify `GET /v1/healthcheck` and `GET /metrics` respond and Prometheus is scraping Watchdog.
- Verify `GET /v1/servers` returns `200` with the configured bearer token and `401` without it. A fresh database can return an empty server list.

After the initial production rollout passes these checks, record the deployment date and verification result in [#204](https://github.com/OneBusAway/watchdog/issues/204).
