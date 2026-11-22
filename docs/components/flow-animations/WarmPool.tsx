"use client";

import type { CSSProperties, ReactNode } from "react";
import { Flow, defineTrack, type FlowKeyframe, type FlowTrack } from "@/components/flow";
import common from "./common.module.css";
import styles from "./warm-pool.module.css";

const STAGE_W = 320;
const STAGE_H = 210;

const PROVIDER = { x: 14, y: 76, w: 80, h: 48 };
const PROVIDER_C = { x: PROVIDER.x + PROVIDER.w / 2, y: PROVIDER.y + PROVIDER.h / 2 };
const CALL_LABEL = { x: PROVIDER_C.x, y: PROVIDER.y + PROVIDER.h + 10 };
const JUNCTION_X = 110;
const POOL = { x: 124, y: 24, w: 184, h: 64 };
const ROW = { x: 124, y: 122, w: 184, h: 64 };
const POD = { w: 60, h: 34 };
const LANES = [POOL.x + 44, POOL.x + 140];
const POOL_Y = POOL.y + 38;
const ROW_Y = ROW.y + 38;
const LOCK = { x: JUNCTION_X, y: (PROVIDER_C.y + ROW_Y) / 2 };

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

const TO_POOL: Pt[] = [
  [PROVIDER.x + PROVIDER.w + 2, PROVIDER_C.y],
  [JUNCTION_X, PROVIDER_C.y],
  [JUNCTION_X, POOL_Y],
  [POOL.x - 2, POOL_Y],
];
const TO_ROW: Pt[] = [
  [PROVIDER.x + PROVIDER.w + 2, PROVIDER_C.y],
  [JUNCTION_X, PROVIDER_C.y],
  [JUNCTION_X, ROW_Y],
  [ROW.x - 2, ROW_Y],
];

const CREATE_A = 800;
const CLAIM_A = arrival(CREATE_A, TO_POOL);
const BOUND_A = CLAIM_A + 650;
const TLS_A = BOUND_A + 150;
const SPAWN_A2 = BOUND_A + 200;
const READY_A2 = SPAWN_A2 + 1500;
const EXEC_A = TLS_A + 900;
const EXEC_LANDS = arrival(EXEC_A, TO_ROW);
const EXEC_DONE = EXEC_LANDS + 900;
const CREATE_B = EXEC_DONE + 500;
const CLAIM_B = arrival(CREATE_B, TO_POOL);
const BOUND_B = CLAIM_B + 650;
const SPAWN_B2 = BOUND_B + 200;
const READY_B2 = SPAWN_B2 + 1500;
const IDLE_A = READY_B2 + 300;
const EXPIRED_A = IDLE_A + 900;
const FADE_AT = EXPIRED_A + 1200;
const DURATION = Math.ceil((FADE_AT + 800) / 100) * 100;
const POSTER_TIME = EXEC_LANDS + 400;

const fadeOut = (x: number, y: number): FlowKeyframe[] => [
  { t: FADE_AT, x, y, ease: "hold" },
  { t: FADE_AT + 450, x, y, opacity: 0, ease: "linear" },
];

const claimedPod = (id: string, lane: number, claimAt: number, boundAt: number, busy: [number, number] | null, end: FlowKeyframe[]) =>
  defineTrack(id, [
    { t: 0, x: lane, y: POOL_Y, state: "ready" },
    { t: claimAt, x: lane, y: POOL_Y, state: "claimed", ease: "hold" },
    { t: boundAt, x: lane, y: ROW_Y, state: "bound", ease: "inOut" },
    ...(busy
      ? [
          { t: busy[0], x: lane, y: ROW_Y, state: "busy", ease: "hold" as const },
          { t: busy[1], x: lane, y: ROW_Y, state: "bound", ease: "hold" as const },
        ]
      : []),
    ...end,
  ]);

const replacementPod = (id: string, lane: number, spawnAt: number, readyAt: number) =>
  defineTrack(id, [
    { t: spawnAt, x: lane, y: POOL_Y, opacity: 0, state: "starting" },
    { t: spawnAt + 300, x: lane, y: POOL_Y, opacity: 1, ease: "linear" },
    { t: readyAt, x: lane, y: POOL_Y, state: "ready", ease: "hold" },
    ...fadeOut(lane, POOL_Y),
  ]);

const POD_TRACKS: FlowTrack[] = [
  claimedPod("pod-a", LANES[0], CLAIM_A, BOUND_A, [EXEC_LANDS, EXEC_DONE], [
    { t: IDLE_A, x: LANES[0], y: ROW_Y, state: "stopped", ease: "hold" },
    { t: EXPIRED_A, x: LANES[0], y: ROW_Y, opacity: 0, ease: "linear" },
  ]),
  claimedPod("pod-b", LANES[1], CLAIM_B, BOUND_B, null, fadeOut(LANES[1], ROW_Y)),
  replacementPod("pod-a2", LANES[0], SPAWN_A2, READY_A2),
  replacementPod("pod-b2", LANES[1], SPAWN_B2, READY_B2),
];

const MSG_TRACKS: FlowTrack[] = [
  defineTrack("create-a", alongPath(CREATE_A, TO_POOL, "call")),
  defineTrack("exec-a", alongPath(EXEC_A, TO_ROW, "call")),
  defineTrack("create-b", alongPath(CREATE_B, TO_POOL, "call")),
];

const label = (id: string, from: number, to: number, x: number, y: number) =>
  defineTrack(id, [
    { t: from - 150, x, y, opacity: 0 },
    { t: from, x, y, opacity: 1, ease: "linear" },
    { t: to - 150, x, y, ease: "hold" },
    { t: to, x, y, opacity: 0, ease: "linear" },
  ]);

const LABELS = [
  { text: "create()", track: label("l-create-a", CREATE_A - 100, BOUND_A, CALL_LABEL.x, CALL_LABEL.y) },
  { text: "exec()", track: label("l-exec-a", EXEC_A - 100, EXEC_DONE, CALL_LABEL.x, CALL_LABEL.y) },
  { text: "create()", track: label("l-create-b", CREATE_B - 100, BOUND_B, CALL_LABEL.x, CALL_LABEL.y) },
  { text: "idle ttl", track: label("l-idle", IDLE_A, EXPIRED_A + 300, LANES[0], ROW_Y + 26) },
  { text: "starting", track: label("l-start-a2", SPAWN_A2 + 300, READY_A2, LANES[0], POOL_Y + 26) },
  { text: "starting", track: label("l-start-b2", SPAWN_B2 + 300, READY_B2, LANES[1], POOL_Y + 26) },
];

const LOCK_TRACK = defineTrack("lock", [
  { t: TLS_A - 150, ...LOCK, opacity: 0, scale: 0.6 },
  { t: TLS_A, ...LOCK, opacity: 1, scale: 1, ease: "out" },
  ...fadeOut(LOCK.x, LOCK.y),
]);

const PROVIDER_TRACK = defineTrack("provider", [
  { t: 0, ...PROVIDER_C, state: "ready" },
  { t: CREATE_A - 100, ...PROVIDER_C, state: "busy", ease: "hold" },
  { t: BOUND_A, ...PROVIDER_C, state: "ready", ease: "hold" },
  { t: EXEC_A - 100, ...PROVIDER_C, state: "busy", ease: "hold" },
  { t: EXEC_DONE, ...PROVIDER_C, state: "ready", ease: "hold" },
  { t: CREATE_B - 100, ...PROVIDER_C, state: "busy", ease: "hold" },
  { t: BOUND_B, ...PROVIDER_C, state: "ready", ease: "hold" },
  { t: DURATION, ...PROVIDER_C, ease: "hold" },
]);

const fine = { fill: "none", strokeWidth: 1, vectorEffect: "non-scaling-stroke" } as const;
const stroke = { fill: "none", strokeWidth: 1.5, vectorEffect: "non-scaling-stroke" } as const;
const pts = (p: Pt[]) => p.map(([x, y]) => `${x},${y}`).join(" ");

const Chrome = () => (
  <svg viewBox={`0 0 ${STAGE_W} ${STAGE_H}`} aria-hidden="true">
    <rect {...PROVIDER} width={PROVIDER.w} height={PROVIDER.h} className={common.chromeStroke} {...stroke} />
    <rect {...POOL} width={POOL.w} height={POOL.h} className={common.chromeFrame} {...fine} />
    <rect {...ROW} width={ROW.w} height={ROW.h} className={common.chromeFrame} {...fine} />
    <polyline points={pts(TO_POOL)} className={common.chromeDash} {...fine} />
    <polyline points={pts(TO_ROW)} className={common.chromeDash} {...fine} />
    {LANES.map((x) => (
      <line key={x} x1={x} y1={POOL_Y + POD.h / 2 + 2} x2={x} y2={ROW_Y - POD.h / 2 - 2} className={common.chromeDash} {...fine} />
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

const Pod = ({ label }: { label: string }) => (
  <div className={`${common.box} ${styles.pod}`}>
    <div className={common.boxHeader}>
      <span className={common.boxDot} />
      {label}
    </div>
  </div>
);

const ARIA_LABEL =
  "Animated diagram of the Kubernetes driver. The Brickie provider keeps a warm pool of two ready sandbox pods. A create call claims one pod, which moves into the sandboxes row and gets a mutual TLS lock on its link to the provider, while the pool starts a replacement. An exec call goes straight to the bound pod. A second create claims the other pod. After the idle TTL the first sandbox is removed.";

export const WarmPool = ({ style }: { style?: CSSProperties }) => (
  <div className={common.wrap} style={style}>
    <Flow.Root duration={DURATION} posterTime={POSTER_TIME} className={common.root} aria-label={ARIA_LABEL}>
      <Flow.Stage width={STAGE_W} height={STAGE_H} className={common.stage}>
        <Chrome />
        <StageLabel x={PROVIDER_C.x} y={PROVIDER.y - 12}>
          brickie worker
        </StageLabel>
        <StageLabel x={POOL.x + POOL.w / 2} y={POOL.y - 12} muted>
          warm pool
        </StageLabel>
        <StageLabel x={ROW.x + ROW.w / 2} y={ROW.y - 12} muted>
          sandboxes
        </StageLabel>
        <Flow.Token track={PROVIDER_TRACK}>
          <div className={styles.providerDot} />
        </Flow.Token>
        {POD_TRACKS.map((track) => (
          <Flow.Token key={track.id} track={track}>
            <Pod label="pod" />
          </Flow.Token>
        ))}
        <Flow.Token track={LOCK_TRACK}>
          <div className={styles.lock}>mTLS</div>
        </Flow.Token>
        {MSG_TRACKS.map((track) => (
          <Flow.Token key={track.id} track={track}>
            <div className={common.msg} />
          </Flow.Token>
        ))}
        {LABELS.map(({ text, track }) => (
          <Flow.Token key={track.id} track={track}>
            <div className={common.callLabel}>{text}</div>
          </Flow.Token>
        ))}
      </Flow.Stage>
    </Flow.Root>
  </div>
);
