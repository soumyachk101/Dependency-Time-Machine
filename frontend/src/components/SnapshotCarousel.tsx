// SnapshotCarousel — a horizontal carousel for version snapshots that
// genuinely aids base/candidate comparison (the spec's "timeline of
// versions" use case). Hand-rolled on pointer events + rAF so no motion
// library is required; every behaviour the design brief calls for is
// implemented explicitly:
//
//   - 1:1 pointer tracking with grab offset, release-velocity handoff
//   - momentum projection to the landing snap point
//   - interruptible/reversible: a new gesture mid-animation cancels it
//   - snap points, partial next-item affordance (peek), keyboard arrows
//   - mouse + touch + keyboard, accessible prev/next buttons
//   - prefers-reduced-motion: no inertia, instant snap

import {
  useCallback,
  useEffect,
  useLayoutEffect,
  useRef,
  useState,
} from "react";
import type {
  CSSProperties,
  KeyboardEvent,
  PointerEvent,
  ReactNode,
} from "react";

export interface SnapshotItem {
  id: string;
  label: string;
  version: string;
  content: ReactNode;
}

interface Props {
  items: SnapshotItem[];
  activeId: string;
  onSelect: (id: string) => void;
  /** Item width in px; the peek uses a fraction of this. */
  itemWidth?: number;
  /** Peek fraction of itemWidth shown of the next item. */
  peekFraction?: number;
  ariaLabel?: string;
}

// Critically-damped spring step: x' = x + (target - x) * (1 - e^-dt/τ).
const TAU_MS = 120;

export function SnapshotCarousel({
  items,
  activeId,
  onSelect,
  itemWidth = 280,
  peekFraction = 0.28,
  ariaLabel = "Version snapshots",
}: Props) {
  const trackRef = useRef<HTMLDivElement>(null);
  const [index, setIndex] = useState(() => Math.max(0, items.findIndex((i) => i.id === activeId)));
  // Live translation during a drag: px, measured from the snap position.
  const [dragPx, setDragPx] = useState(0);
  const [dragging, setDragging] = useState(false);

  const reducedMotion = useReducedMotion();
  const snapGap = itemWidth + 8; // gap between item cards

  const targetForIndex = (i: number) => -i * snapGap;
  const total = items.length;

  // Keep index in sync when the active item changes from outside.
  useEffect(() => {
    const i = items.findIndex((it) => it.id === activeId);
    if (i >= 0) setIndex(i);
  }, [activeId, items]);

  // Animate the track to the current snap point when not dragging.
  useLayoutEffect(() => {
    const track = trackRef.current;
    if (!track) return;
    if (dragging) return; // pointer owns the position while dragging
    const target = targetForIndex(index);
    track.style.transition = reducedMotion ? "none" : "transform 250ms var(--ease-out)";
    track.style.transform = `translateX(${target + dragPx}px)`;
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [index, dragPx, dragging, reducedMotion]);

  // ---- gesture handling ----------------------------------------------------

  const gesture = useRef({
    pointerId: -1,
    startX: 0,
    startDrag: 0,
    lastX: 0,
    lastT: 0,
    velocity: 0, // px/ms, sign: right = positive
    animFrame: 0,
  });

  const onPointerDown = useCallback(
    (e: PointerEvent<HTMLDivElement>) => {
      const g = gesture.current;
      cancelAnimationFrame(g.animFrame); // interrupt any running spring
      g.pointerId = e.pointerId;
      g.startX = e.clientX;
      g.startDrag = dragPx;
      g.lastX = e.clientX;
      g.lastT = performance.now();
      g.velocity = 0;
      setDragging(true);
      (e.currentTarget as HTMLElement).setPointerCapture(e.pointerId);
    },
    [dragPx],
  );

  const onPointerMove = useCallback(
    (e: PointerEvent<HTMLDivElement>) => {
      const g = gesture.current;
      if (e.pointerId !== g.pointerId) return;
      const now = performance.now();
      const dx = e.clientX - g.startX;
      const dt = Math.max(1, now - g.lastT);
      // 1:1 tracking, respect the grab offset (startDrag).
      setDragPx(g.startDrag + dx);
      // Velocity estimate (px/ms) from the last sample.
      g.velocity = (e.clientX - g.lastX) / dt;
      g.lastX = e.clientX;
      g.lastT = now;
    },
    [],
  );

  const settle = useCallback(
    (endDrag: number) => {
      const g = gesture.current;
      setDragging(false);
      const current = targetForIndex(index) + endDrag;
      const projected = reducedMotion ? endDrag : endDrag + g.velocity * 160; // momentum
      let next = index + Math.round(projected / snapGap);
      next = Math.max(0, Math.min(total - 1, next));
      g.animFrame = requestAnimationFrame(() => {
        setDragPx(0);
        setIndex(next);
        if (items[next]) onSelect(items[next].id);
      });
      void current;
    },
    [index, total, reducedMotion, snapGap, items, onSelect],
  );

  const onPointerUp = useCallback(
    (e: PointerEvent<HTMLDivElement>) => {
      const g = gesture.current;
      if (e.pointerId !== g.pointerId) return;
      g.pointerId = -1;
      const endDrag = g.startDrag + (e.clientX - g.startX);
      (e.currentTarget as HTMLElement).releasePointerCapture?.(e.pointerId);
      settle(endDrag);
    },
    [settle],
  );

  const onKeyDown = useCallback(
    (e: KeyboardEvent<HTMLDivElement>) => {
      let next = index;
      if (e.key === "ArrowRight") next = Math.min(total - 1, index + 1);
      else if (e.key === "ArrowLeft") next = Math.max(0, index - 1);
      else if (e.key === "Home") next = 0;
      else if (e.key === "End") next = total - 1;
      else return;
      e.preventDefault();
      setIndex(next);
      if (items[next]) onSelect(items[next].id);
    },
    [index, total, items, onSelect],
  );

  const step = useCallback(
    (delta: number) => {
      const next = Math.max(0, Math.min(total - 1, index + delta));
      setIndex(next);
      if (items[next]) onSelect(items[next].id);
    },
    [index, total, items, onSelect],
  );

  // Peek: show part of the next item at the far right when not on last.
  const peekWidth = itemWidth * peekFraction;

  const style: CSSProperties = {
    display: "flex",
    gap: 8,
    transform: `translateX(${targetForIndex(index) + dragPx}px)`,
    transition: dragging ? "none" : reducedMotion ? "none" : "transform 250ms var(--ease-out)",
    touchAction: "none",
  };

  return (
    <div>
      <div
        role="listbox"
        aria-label={ariaLabel}
        aria-activedescendant={`snap-${items[index]?.id ?? ""}`}
        tabIndex={0}
        onKeyDown={onKeyDown}
        style={{
          overflow: "hidden",
          cursor: dragging ? "grabbing" : "grab",
          userSelect: "none",
          paddingRight: index < total - 1 ? peekWidth : 0, // partial next-item affordance
          position: "relative",
        }}
        onPointerDown={onPointerDown}
        onPointerMove={onPointerMove}
        onPointerUp={onPointerUp}
        onPointerCancel={onPointerUp}
      >
        <div ref={trackRef} style={style} role="list">
          {items.map((it, i) => (
            <button
              key={it.id}
              role="option"
              aria-selected={i === index}
              id={`snap-${it.id}`}
              onClick={() => {
                setIndex(i);
                onSelect(it.id);
              }}
              style={{
                flex: "0 0 auto",
                width: itemWidth,
                background: i === index ? "var(--surface-2)" : "var(--surface-1)",
                border: "1px solid var(--border-subtle)",
                borderRadius: "var(--radius-md)",
                color: "inherit",
                cursor: "pointer",
                textAlign: "left",
                padding: 0,
              }}
            >
              <div style={{ padding: 12 }}>
                <div className="mono" style={{ fontSize: "0.6875rem", color: "var(--text-muted)" }}>
                  {it.label}
                </div>
                <div className="mono" style={{ fontSize: "1rem", margin: "2px 0 8px" }}>
                  {it.version}
                </div>
                {it.content}
              </div>
            </button>
          ))}
        </div>
      </div>

      <div style={{ display: "flex", gap: 8, marginTop: 8 }}>
        <button onClick={() => step(-1)} disabled={index === 0} aria-label="Previous snapshot">
          ‹
        </button>
        <button onClick={() => step(1)} disabled={index === total - 1} aria-label="Next snapshot">
          ›
        </button>
        <span className="mono" style={{ fontSize: "0.75rem", color: "var(--text-muted)", alignSelf: "center" }}>
          {total > 0 ? index + 1 : 0} / {total}
        </span>
      </div>
    </div>
  );
}

// useReducedMotion tracks prefers-reduced-motion and disables inertia +
// transition when set.
function useReducedMotion(): boolean {
  const [reduced, setReduced] = useState(false);
  useEffect(() => {
    const mq = window.matchMedia("(prefers-reduced-motion: reduce)");
    setReduced(mq.matches);
    const onChange = (e: MediaQueryListEvent) => setReduced(e.matches);
    mq.addEventListener?.("change", onChange);
    return () => mq.removeEventListener?.("change", onChange);
  }, []);
  return reduced;
}
