"use client";

import type { CSSProperties, ReactNode } from "react";
import { Flow, defineTrack, type FlowKeyframe, type FlowTrack } from "@/components/flow";
import common from "./common.module.css";
import styles from "./turn-lifecycle.module.css";

const STAGE_W = 320;
const STAGE_H = 200;

const TASK = { x: 36, y: 100 };
const CALL_LABEL = { x: 36, y: 118 };
const HATCHET = { x: 78, y: 84, w: 64, h: 32 };
const JUNCTION_X = 160;
const PROVIDER = { x: 176, y: 30, w: 124, h: 40 };
const SANDBOX = { x: 176, y: 94, w: 124, h: 52 };
const STORE = { x: 176, y: 166, w: 124, h: 26 };
const PHASE = { x: 110, y: 138 };

const center = (b: { x: number; y: number; w: number; h: number }) => ({ x: b.x + b.w / 2, y: b.y + b.h / 2 });
const HATCHET_C = center(HATCHET);
const PROVIDER_C = center(PROVIDER);
const SANDBOX_C = center(SANDBOX);
const STORE_C = center(STORE);

const RESTORE_X = SANDBOX.x + 48;
const CHECKPOINT_X = SANDBOX.x + SANDBOX.w - 48;
const barX = (i: number) => SANDBOX.x + 12 + i * 9;
const BAR_Y = SANDBOX.y + 38;
const snapX = (i: number) => STORE.x + 14 + i * 14;
const SNAP_Y = STORE_C.y;

const SPEED = 0.13;

type Pt = [number, number];

const alongPath = (t0: number, pts: Pt[], state: string): FlowKeyframe[] => {
  let travelled = 0;
  return pts.map(([x, y], i) => {
    if (i > 0) travelled += Math.hypot(x - pts[i - 1][0], y - pts[i - 1][1]);
    const kf: FlowKeyframe = {
      t: Math.round(t0 + travelled / SPEED),
      x,
      y,
      opacity: i === 0 || i === pts.length - 1 ? 0 : 1,
      ease: "linear",
    };
    return i === 0 ? { ...kf, state } : kf;
  });
};

const pathLength = (pts: Pt[]) => pts.slice(1).reduce((sum, [x, y], i) => sum + Math.hypot(x - pts[i][0], y - pts[i][1]), 0);
const arrival = (t0: number, pts: Pt[]) => Math.round(t0 + pathLength(pts) / SPEED);

const TASK_TO_HATCHET: Pt[] = [
  [TASK.x + 14, TASK.y],
  [TASK.x + 20, TASK.y],
  [HATCHET.x - 2, TASK.y],
];
const HATCHET_TO_PROVIDER: Pt[] = [
  [HATCHET.x + HATCHET.w + 2, HATCHET_C.y],
  [JUNCTION_X, HATCHET_C.y],
  [JUNCTION_X, PROVIDER_C.y],
  [PROVIDER.x - 2, PROVIDER_C.y],
];
const PROVIDER_TO_SANDBOX: Pt[] = [
  [PROVIDER_C.x, PROVIDER.y + PROVIDER.h + 2],
  [PROVIDER_C.x, SANDBOX.y - 2],
];
const STORE_TO_SANDBOX: Pt[] = [
  [RESTORE_X, STORE.y - 2],
  [RESTORE_X, SANDBOX.y + SANDBOX.h + 2],
];
const SANDBOX_TO_STORE: Pt[] = [
  [CHECKPOINT_X, SANDBOX.y + SANDBOX.h + 2],
  [CHECKPOINT_X, STORE.y - 2],
];
const reverse = (pts: Pt[]): Pt[] => [...pts].reverse();

const CALL_AT = 700;
const QUEUED = arrival(CALL_AT, TASK_TO_HATCHET);
const CLAIM_AT = QUEUED + 200;
const CLAIMED = arrival(CLAIM_AT, HATCHET_TO_PROVIDER);
const DIAL_AT = CLAIMED + 150;
const DIALED = arrival(DIAL_AT, PROVIDER_TO_SANDBOX);
const RESTORE_AT = DIALED + 100;
const RESTORED = arrival(RESTORE_AT, STORE_TO_SANDBOX);
const EXEC_AT = RESTORED + 200;
const EXEC_DONE = EXEC_AT + 1500;
const CHECKPOINT_AT = EXEC_DONE + 150;
const STORED = arrival(CHECKPOINT_AT, SANDBOX_TO_STORE);
const POINTER_AT = STORED + 300;
const RESULT_AT = POINTER_AT + 300;
const RESULT_UP = arrival(RESULT_AT, reverse(PROVIDER_TO_SANDBOX));
const RESULT_BACK = arrival(RESULT_UP, reverse(HATCHET_TO_PROVIDER));
const RESULT_HOME = arrival(RESULT_BACK + 150, reverse(TASK_TO_HATCHET));

const FADE_AT = RESULT_HOME + 1600;
const DURATION = Math.ceil((FADE_AT + 800) / 100) * 100;
const POSTER_TIME = EXEC_AT + 900;

const fadeOut = (x: number, y: number): FlowKeyframe[] => [
  { t: FADE_AT, x, y, ease: "hold" },
  { t: FADE_AT + 450, x, y, opacity: 0, ease: "linear" },
];

const MSG_TRACKS: FlowTrack[] = [
  defineTrack("call-in", alongPath(CALL_AT, TASK_TO_HATCHET, "call")),
  defineTrack("claim", alongPath(CLAIM_AT, HATCHET_TO_PROVIDER, "call")),
  defineTrack("dial", alongPath(DIAL_AT, PROVIDER_TO_SANDBOX, "call")),
  defineTrack("restore", alongPath(RESTORE_AT, STORE_TO_SANDBOX, "data")),
  defineTrack("checkpoint", alongPath(CHECKPOINT_AT, SANDBOX_TO_STORE, "data")),
  defineTrack("result-up", alongPath(RESULT_AT, reverse(PROVIDER_TO_SANDBOX), "result")),
  defineTrack("result-back", alongPath(RESULT_UP, reverse(HATCHET_TO_PROVIDER), "result")),
  defineTrack("result-home", alongPath(RESULT_BACK + 150, reverse(TASK_TO_HATCHET), "result")),
];

const TASK_TRACK = defineTrack("task", [
  { t: 0, ...TASK, state: "idle" },
  { t: CALL_AT - 200, ...TASK, state: "running", ease: "hold" },
  { t: RESULT_HOME, ...TASK, state: "done", ease: "hold" },
  { t: FADE_AT + 250, ...TASK, state: "idle", ease: "hold" },
  { t: DURATION, ...TASK, ease: "hold" },
]);

const label = (id: string, from: number, to: number, x: number, y: number) =>
  defineTrack(id, [
    { t: from - 150, x, y, opacity: 0 },
    { t: from, x, y, opacity: 1, ease: "linear" },
    { t: to - 150, x, y, ease: "hold" },
    { t: to, x, y, opacity: 0, ease: "linear" },
  ]);

const CALL_LABEL_TRACK = label("call-label", CALL_AT - 200, RESULT_HOME, CALL_LABEL.x, CALL_LABEL.y);
const RESULT_LABEL_TRACK = label("result-label", RESULT_HOME, FADE_AT, CALL_LABEL.x, CALL_LABEL.y);

const PHASES = [
  { name: "queued", from: QUEUED, to: CLAIMED },
  { name: "claim", from: CLAIMED, to: RESTORE_AT },
  { name: "restore", from: RESTORE_AT, to: EXEC_AT },
  { name: "exec", from: EXEC_AT, to: CHECKPOINT_AT },
  { name: "checkpoint", from: CHECKPOINT_AT, to: RESULT_AT },
  { name: "result", from: RESULT_AT, to: RESULT_HOME + 400 },
];
const PHASE_TRACKS = PHASES.map((p) => label(`phase-${p.name}`, p.from, p.to, PHASE.x, PHASE.y));

const SLOT_TRACK = defineTrack("slot", [
  { t: QUEUED - 40, ...HATCHET_C, opacity: 0, state: "queued" },
  { t: QUEUED, ...HATCHET_C, opacity: 1, ease: "linear" },
  { t: CLAIMED, ...HATCHET_C, state: "running", ease: "hold" },
  { t: RESULT_BACK, ...HATCHET_C, state: "done", ease: "hold" },
  ...fadeOut(HATCHET_C.x, HATCHET_C.y),
]);

const PROVIDER_TRACK = defineTrack("provider", [
  { t: 0, ...PROVIDER_C, state: "ready" },
  { t: CLAIMED, ...PROVIDER_C, state: "busy", ease: "hold" },
  { t: RESULT_UP, ...PROVIDER_C, state: "ready", ease: "hold" },
  { t: DURATION, ...PROVIDER_C, ease: "hold" },
]);

const SANDBOX_TRACK = defineTrack("sandbox", [
  { t: 0, ...SANDBOX_C, state: "off" },
  { t: DIALED, ...SANDBOX_C, state: "running", ease: "hold" },
  { t: EXEC_AT, ...SANDBOX_C, state: "busy", ease: "hold" },
  { t: EXEC_DONE, ...SANDBOX_C, state: "running", ease: "hold" },
  { t: FADE_AT + 250, ...SANDBOX_C, state: "off", ease: "hold" },
  { t: DURATION, ...SANDBOX_C, ease: "hold" },
]);

const bar = (id: string, at: number, i: number, state: string) =>
  defineTrack(id, [
    { t: at - 40, x: barX(i), y: BAR_Y, opacity: 0, state },
    { t: at, x: barX(i), y: BAR_Y, opacity: 1, ease: "linear" },
    { t: at + 600, x: barX(i), y: BAR_Y, state: "settled", ease: "hold" },
    ...fadeOut(barX(i), BAR_Y),
  ]);
const BAR_TRACKS: FlowTrack[] = [
  bar("bar-0", RESTORED, 0, "restored"),
  bar("bar-1", RESTORED + 80, 1, "restored"),
  bar("bar-2", RESTORED + 160, 2, "restored"),
  bar("bar-3", EXEC_AT + 700, 3, "fresh"),
  bar("bar-4", EXEC_AT + 1100, 4, "fresh"),
];

const SNAPSHOT_TRACKS: FlowTrack[] = [
  defineTrack("snap-old", [
    { t: 0, x: snapX(0), y: SNAP_Y, state: "stored" },
    { t: RESTORE_AT, x: snapX(0), y: SNAP_Y, state: "read", ease: "hold" },
    { t: RESTORED, x: snapX(0), y: SNAP_Y, state: "stored", ease: "hold" },
    { t: DURATION, x: snapX(0), y: SNAP_Y, ease: "hold" },
  ]),
  defineTrack("snap-new", [
    { t: STORED - 40, x: snapX(1), y: SNAP_Y, opacity: 0, state: "fresh" },
    { t: STORED, x: snapX(1), y: SNAP_Y, opacity: 1, ease: "linear" },
    { t: STORED + 600, x: snapX(1), y: SNAP_Y, state: "stored", ease: "hold" },
    ...fadeOut(snapX(1), SNAP_Y),
  ]),
];

const POINTER_TRACK = defineTrack("latest", [
  { t: 0, x: snapX(0), y: SNAP_Y + 8 },
  { t: POINTER_AT, x: snapX(0), y: SNAP_Y + 8, ease: "hold" },
  { t: POINTER_AT + 350, x: snapX(1), y: SNAP_Y + 8, ease: "out" },
  { t: FADE_AT + 450, x: snapX(1), y: SNAP_Y + 8, ease: "hold" },
  { t: FADE_AT + 500, x: snapX(0), y: SNAP_Y + 8, ease: "hold" },
  { t: DURATION, x: snapX(0), y: SNAP_Y + 8, ease: "hold" },
]);

const fine = { fill: "none", strokeWidth: 1, vectorEffect: "non-scaling-stroke" } as const;
const stroke = { fill: "none", strokeWidth: 1.5, vectorEffect: "non-scaling-stroke" } as const;
const pts = (p: Pt[]) => p.map(([x, y]) => `${x},${y}`).join(" ");

const Chrome = () => (
  <svg viewBox={`0 0 ${STAGE_W} ${STAGE_H}`} aria-hidden="true">
    <rect {...HATCHET} width={HATCHET.w} height={HATCHET.h} className={common.chromeStroke} {...stroke} />
    <rect {...PROVIDER} width={PROVIDER.w} height={PROVIDER.h} className={common.chromeStroke} {...stroke} />
    <rect {...SANDBOX} width={SANDBOX.w} height={SANDBOX.h} className={common.chromeFrame} {...fine} />
    <rect {...STORE} width={STORE.w} height={STORE.h} className={common.chromeStroke} {...stroke} />
    <polyline points={pts(TASK_TO_HATCHET)} className={common.chromeDash} {...fine} />
    <polyline points={pts(HATCHET_TO_PROVIDER)} className={common.chromeDash} {...fine} />
    <polyline points={pts(PROVIDER_TO_SANDBOX)} className={common.chromeDash} {...fine} />
    <polyline points={pts(STORE_TO_SANDBOX)} className={common.chromeDash} {...fine} />
    <polyline points={pts(SANDBOX_TO_STORE)} className={common.chromeDash} {...fine} />
    <rect x={HATCHET_C.x - 4} y={HATCHET_C.y - 4} width={8} height={8} className={common.chromeSlot} {...fine} />
    {[0, 1].map((i) => (
      <rect key={i} x={snapX(i) - 4} y={SNAP_Y - 4} width={8} height={8} className={common.chromeSlot} {...fine} />
    ))}
  </svg>
);

const StageLabel = ({ x, y, muted = false, children }: { x: number; y: number; muted?: boolean; children: ReactNode }) => (
  <div
    className={`${common.stageLabel} ${muted ? common.labelMuted : ""}`}
    style={{ left: `calc(var(--flow-u) * ${x})`, top: `calc(var(--flow-u) * ${y})` }}
  >
    {children}
  </div>
);

const ARIA_LABEL =
  "Animated diagram of one Brickie turn. A task calls exec. The call becomes a Hatchet task that the Brickie provider claims. The provider dials the sandbox, restores the latest snapshot from the store into it, runs the command, streams a new snapshot back to the store and moves the latest pointer to it. The result then travels back through Hatchet to the task.";

export const TurnLifecycle = ({ style }: { style?: CSSProperties }) => (
  <div className={common.wrap} style={style}>
    <Flow.Root duration={DURATION} posterTime={POSTER_TIME} className={common.root} aria-label={ARIA_LABEL}>
      <Flow.Stage width={STAGE_W} height={STAGE_H} className={common.stage}>
        <Chrome />
        <StageLabel x={TASK.x} y={78}>
          your task
        </StageLabel>
        <StageLabel x={HATCHET_C.x} y={HATCHET.y - 12}>
          hatchet
        </StageLabel>
        <StageLabel x={PROVIDER_C.x} y={PROVIDER.y - 12}>
          brickie worker
        </StageLabel>
        <StageLabel x={SANDBOX.x + 22} y={SANDBOX.y - 10} muted>
          sandbox
        </StageLabel>
        <StageLabel x={STORE.x + STORE.w - 20} y={STORE.y + 9} muted>
          store
        </StageLabel>
        <StageLabel x={SANDBOX.x + 26} y={SANDBOX.y + 8} muted>
          /workspace
        </StageLabel>
        <Flow.Token track={PROVIDER_TRACK}>
          <div className={styles.providerDot} />
        </Flow.Token>
        <Flow.Token track={SANDBOX_TRACK}>
          <div className={styles.sandboxDot} />
        </Flow.Token>
        {BAR_TRACKS.map((track) => (
          <Flow.Token key={track.id} track={track}>
            <div className={common.bar} />
          </Flow.Token>
        ))}
        {SNAPSHOT_TRACKS.map((track) => (
          <Flow.Token key={track.id} track={track}>
            <div className={common.snapshot} />
          </Flow.Token>
        ))}
        <Flow.Token track={POINTER_TRACK}>
          <div className={styles.pointer}>latest</div>
        </Flow.Token>
        <Flow.Token track={SLOT_TRACK}>
          <div className={styles.slot} />
        </Flow.Token>
        {MSG_TRACKS.map((track) => (
          <Flow.Token key={track.id} track={track}>
            <div className={common.msg} />
          </Flow.Token>
        ))}
        <Flow.Token track={TASK_TRACK}>
          <div className={common.pill}>task</div>
        </Flow.Token>
        <Flow.Token track={CALL_LABEL_TRACK}>
          <div className={common.callLabel}>exec(&apos;make test&apos;)</div>
        </Flow.Token>
        <Flow.Token track={RESULT_LABEL_TRACK}>
          <div className={common.callLabel}>{"{ stdout, exitCode }"}</div>
        </Flow.Token>
        {PHASE_TRACKS.map((track, i) => (
          <Flow.Token key={track.id} track={track}>
            <div className={styles.phase}>{PHASES[i].name}</div>
          </Flow.Token>
        ))}
      </Flow.Stage>
    </Flow.Root>
  </div>
);
