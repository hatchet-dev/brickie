import atexit
import os
import subprocess
from typing import Any

from hatchet_sdk import DurableContext, Hatchet
from pydantic import BaseModel

SANDBOX_TASK_NAME = "brickie-sandbox"

_COMMAND_FN = "$command"
_PROMPT_FN = "$prompt"


class TurnInput(BaseModel):
    image: str | None = None
    fn: str
    fnSource: str | None = None
    input: dict[str, Any]
    snapshotId: str | None = None


class TurnResult(BaseModel):
    output: dict[str, Any]
    snapshotId: str


class Item(BaseModel):
    command: str | None = None
    prompt: str | None = None
    from_: str | None = None


_sidecar: subprocess.Popen[bytes] | None = None


def _ensure_local_provider(hatchet: Hatchet) -> None:
    global _sidecar
    if _sidecar is not None or os.environ.get("HATCHET_CLIENT_TOKEN"):
        return
    cfg = hatchet.config
    strategy = getattr(cfg.tls_config.tls_strategy, "value", cfg.tls_config.tls_strategy)
    env = {
        **os.environ,
        "HATCHET_CLIENT_TOKEN": cfg.token,
        "HATCHET_CLIENT_HOST_PORT": cfg.host_port,
        "HATCHET_CLIENT_TLS_STRATEGY": str(strategy),
    }
    binary = os.environ.get("BRICKIE_BIN", "brickie")
    try:
        _sidecar = subprocess.Popen([binary, "worker", "--driver", "local"], env=env)
    except FileNotFoundError:
        raise RuntimeError(
            f"brickie: local sandbox provider binary not found ({binary}); "
            "install it with: go install github.com/hatchet-dev/brickie/cmd/brickie@latest (or set BRICKIE_BIN)"
        )
    atexit.register(_sidecar.terminate)


def _unwrap(result: dict[str, Any]) -> TurnResult:
    if SANDBOX_TASK_NAME in result and isinstance(result[SANDBOX_TASK_NAME], dict):
        result = result[SANDBOX_TASK_NAME]
    return TurnResult(**result)


class Sandbox:
    def __init__(self, hatchet: Hatchet, image: str | None = None):
        self.image = image
        self._stub = hatchet.workflow(name=SANDBOX_TASK_NAME, input_validator=TurnInput)
        _ensure_local_provider(hatchet)

    def _turn(self, fn: str, input: dict[str, Any], from_: str | None) -> TurnInput:
        return TurnInput(image=self.image, fn=fn, input=input, snapshotId=from_)

    async def execute_command(self, ctx: DurableContext, command: str, from_: str | None = None) -> TurnResult:
        return _unwrap(await self._stub.aio_run(self._turn(_COMMAND_FN, {"cmd": command}, from_)))

    async def execute_prompt(self, ctx: DurableContext, prompt: str, from_: str | None = None) -> TurnResult:
        return _unwrap(await self._stub.aio_run(self._turn(_PROMPT_FN, {"prompt": prompt}, from_)))

    async def execute_all(self, ctx: DurableContext, items: list[Item]) -> list[TurnResult]:
        runs = []
        for item in items:
            if item.command is not None:
                turn = self._turn(_COMMAND_FN, {"cmd": item.command}, item.from_)
            elif item.prompt is not None:
                turn = self._turn(_PROMPT_FN, {"prompt": item.prompt}, item.from_)
            else:
                raise ValueError("item must set command or prompt")
            runs.append(self._stub.create_bulk_run_item(input=turn))
        results = await self._stub.aio_run_many(runs)
        return [_unwrap(r) for r in results]
