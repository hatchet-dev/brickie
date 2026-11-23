# Quickstart

Get up and running quickly with a local Hatchet engine and a Docker-based sandbox session. You need a running Docker daemon, Node 24 and [pnpm](https://pnpm.io/installation).

> [!NOTE]
> [Hatchet Cloud](https://cloud.hatchet.run) is an easier way to get started. Create a tenant API token, export it as `HATCHET_CLIENT_TOKEN` and skip to [Start the Brickie provider](#start-the-brickie-provider). The same works on a self-hosted Hatchet v0.110.0 or newer with `SERVER_GRPC_OPERATORS_ENABLED=true`.

## Start Hatchet

Save this as `docker-compose.yml` in an empty directory. It starts [Hatchet Lite](https://docs.hatchet.run/self-hosting/hatchet-lite?utm_source=brickie-docs&utm_medium=referral&utm_campaign=brickie-launch&utm_content=quickstart) with Postgres and gRPC operators turned on.

```yaml
name: brickie
services:
  postgres:
    image: postgres:17
    command: postgres -c 'max_connections=200'
    restart: always
    environment:
      POSTGRES_USER: hatchet
      POSTGRES_PASSWORD: hatchet
      POSTGRES_DB: hatchet
    volumes:
      - brickie_postgres_data:/var/lib/postgresql/data
    healthcheck:
      test: ["CMD-SHELL", "pg_isready -d hatchet -U hatchet"]
      interval: 10s
      timeout: 10s
      retries: 5
      start_period: 10s
  hatchet:
    image: ghcr.io/hatchet-dev/hatchet/hatchet-lite:v0.110.5
    ports:
      - "127.0.0.1:8888:8888"
      - "127.0.0.1:7077:7077"
    depends_on:
      postgres:
        condition: service_healthy
    environment:
      DATABASE_URL: "postgresql://hatchet:hatchet@postgres:5432/hatchet?sslmode=disable"
      SERVER_AUTH_COOKIE_DOMAIN: localhost
      SERVER_AUTH_COOKIE_INSECURE: "t"
      SERVER_GRPC_BIND_ADDRESS: "0.0.0.0"
      SERVER_GRPC_INSECURE: "t"
      SERVER_GRPC_BROADCAST_ADDRESS: localhost:7077
      SERVER_GRPC_PORT: "7077"
      SERVER_URL: http://localhost:8888
      SERVER_AUTH_SET_EMAIL_VERIFIED: "t"
      SERVER_GRPC_OPERATORS_ENABLED: "t"
      SERVER_ENABLE_WORKER_RETENTION: "t"
      SERVER_INTERNAL_CLIENT_INTERNAL_GRPC_BROADCAST_ADDRESS: localhost:7077
    healthcheck:
      test:
        ["CMD-SHELL", "wget -q -O- http://localhost:8888/api/ready || exit 1"]
      interval: 5s
      timeout: 5s
      retries: 20
      start_period: 10s
    volumes:
      - brickie_hatchet_config:/config
volumes:
  brickie_postgres_data:
  brickie_hatchet_config:
```

```bash
docker compose up -d --wait
```

The Hatchet dashboard is at [http://localhost:8888](http://localhost:8888). Log in with `admin@example.com` and `Admin123!!`.

## Create a token

Both the provider and your code authenticate to Hatchet with a client token. Hatchet Lite seeds one tenant, so this creates a token for it and puts it in your shell.

```bash
export HATCHET_CLIENT_TOKEN=$(docker compose exec hatchet ./hatchet-admin token create \
  --config /config --tenant-id 707d0855-80ab-4e1f-a156-f1c4546cbf52 | tr -d '[:space:]')
export HATCHET_CLIENT_TLS_STRATEGY=none
```

The second line is for local Hatchet only which serves gRPC without TLS. Leave it out against Hatchet Cloud.

## Start the Brickie provider

Download the archive for your platform from the [releases page](https://github.com/hatchet-dev/brickie/releases/latest), for example `brickie_0.1.7_Darwin_arm64.tar.gz` or `brickie_0.1.7_Linux_x86_64.tar.gz`, and start it in the same shell.

```bash
tar -xzf brickie_*.tar.gz
./brickie worker
```

It pulls `ghcr.io/hatchet-dev/sandboxd` once, connects to Hatchet and logs `connected as grpc operator`.

Sandboxes are containers on your Docker engine. Snapshots go to `~/.brickie/store`. Leave it running and open a second shell with the same exports for the next step.

## Run a sandbox session

Your code needs a client token for the same tenant the provider is connected to. The SDK triggers the `brickie-sandbox` task in the tenant of its token, and only the provider connected to that tenant serves it. A token for another tenant, or a shell without the exports from [Create a token](#create-a-token), gives a run that waits forever for a worker.

```bash
echo "$HATCHET_CLIENT_TOKEN" | cut -c1-12   # prints the start of the token when the exports are in place
mkdir agent && cd agent
pnpm init
pnpm add @hatchet-dev/sandbox @hatchet-dev/typescript-sdk
```

Copy the following code as `agent-session.mts`:

```ts
import { HatchetClient } from "@hatchet-dev/typescript-sdk/v1";
import "@hatchet-dev/sandbox";

const hatchet = HatchetClient.init();

const session = hatchet.durableTask({
  name: "agent-session",
  executionTimeout: "30m",
  fn: async (input: { id: string }) => {
    const sandbox = await hatchet.sandbox.create({
      id: input.id,
      template: "alpine:3",
    });
    await sandbox.files.write("haiku.txt", "an old silent pond");
    const haiku = await sandbox.exec("cat haiku.txt");
    await sandbox.kill();
    return { haiku: haiku.stdout, snapshot: haiku.snapshotId };
  },
});

const worker = await hatchet.worker("agent-worker", { workflows: [session] });
void worker.start();
await worker.waitUntilReady();

console.log(await session.run({ id: "conv-42" }));

await worker.stop();
process.exit(0);
```

`template` is the container image the sandbox runs. Any image with `sh`, `tar` and `gzip` works, from `alpine:3` to your own custom image. The provider's default is `alpine:3` which can be overridden using the `--image` argument.

Run the example via:

```bash
pnpm dlx tsx agent-session.mts
```

```json
{
  "haiku": "an old silent pond",
  "snapshot": "conv-42/0193a2f1-7c3e-7a1b-9d2e-4f5a6b7c8d9e"
}
```

The run shows up in the dashboard under `agent-session`, with one `brickie-sandbox` child run per call. The sandbox is killed at the end but its snapshot stays, so `hatchet.sandbox.connect('conv-42')` from any code with the same token brings it back with `haiku.txt` in place.

The full walkthrough with fork, list and delete is in [`examples/typescript/demo.ts`](../examples/typescript/demo.ts) in the repository.

## Stop it

```bash
docker compose down -v
rm -rf ~/.brickie
```

Stop the provider with Ctrl-C.

## Next

Sandboxes on the local Docker engine are plain containers. That is for local testing only. For production, [install the provider on Kubernetes](install/kubernetes.md), where every sandbox is its own pod under Kata Containers, gVisor or AWS Nitro.
