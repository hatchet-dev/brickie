import asyncio
import json
import os
import sys
from datetime import timedelta

from hatchet_sdk import DurableContext, EmptyModel, Hatchet

from hatchet_sandbox import Item, Sandbox

hatchet = Hatchet() if os.environ.get("HATCHET_CLIENT_TOKEN") else Hatchet.from_embedded()
sbx = Sandbox(hatchet)


@hatchet.durable_task(name="command-session-py", execution_timeout=timedelta(minutes=30))
async def session(input: EmptyModel, ctx: DurableContext) -> dict:
    t1 = await sbx.execute_command(ctx, "echo one > log.txt && uname -s")
    t2 = await sbx.execute_command(ctx, "echo two >> log.txt && wc -l < log.txt", from_=t1.snapshotId)
    a, b = await sbx.execute_all(
        ctx,
        [
            Item(command="echo a >> log.txt && tr '\\n' ' ' < log.txt", from_=t2.snapshotId),
            Item(command="echo b >> log.txt && tr '\\n' ' ' < log.txt", from_=t2.snapshotId),
        ],
    )
    return {"t1": t1.model_dump(), "t2": t2.model_dump(), "a": a.model_dump(), "b": b.model_dump()}


async def main() -> None:
    worker = hatchet.worker("command-worker-py", workflows=[session])
    task = asyncio.create_task(asyncio.to_thread(worker.start))
    result = await session.aio_run()
    print("session result:", json.dumps(result, indent=2))
    ok = result["a"]["output"]["stdout"].strip() == "one two a" and result["b"]["output"]["stdout"].strip() == "one two b"
    print("e2e ok" if ok else "e2e FAILED")
    task.cancel()
    os._exit(0 if ok else 1)


if __name__ == "__main__":
    asyncio.run(main())
