/**
 * Pure helpers for the Arthas terminal readiness watchdog.
 *
 * Kept free of React/DOM so the classification logic can be unit tested
 * independently of the terminal component.
 */

/** Max time to wait for the first real agent frame after the relay is bound. */
export const FIRST_FRAME_TIMEOUT_MS = 6000;

/** Hard cap on automatic reconnect attempts. */
export const MAX_AUTO_RETRIES = 1;

/** Delay before a new WebSocket is opened after closing a silent one. */
export const RECONNECT_DELAY_MS = 200;

const ANSI_PATTERN = /\x1b\[[0-9;]*m/g;
const STATUS_FRAME_PATTERN = /^\[[*+!\-.]\]\s/;

/**
 * A backend status frame is produced by formatTerminalStatus: ANSI color +
 * one of [*] [+] [!] [-] [.] + message + reset + CRLF. These are progress
 * messages, NOT output from the Arthas interactive session.
 */
export function isServerStatusFrame(text: string): boolean {
  // Backend status frames always start with an ANSI color sequence. Requiring
  // it prevents Arthas output that happens to begin with "[*] " from being
  // mistaken for progress text and starving the watchdog.
  if (!text || !text.startsWith('\x1b[')) return false;
  return STATUS_FRAME_PATTERN.test(text.replace(ANSI_PATTERN, ''));
}

/** True when the backend announced that the relay channel was bound. */
export function isRelayReadyFrame(text: string): boolean {
  return text.includes('[+]');
}

/**
 * Decides what to do when the watchdog fires.
 * Returns 'retry' at most MAX_AUTO_RETRIES times, otherwise 'give-up'.
 */
export function decideWatchdogAction(retriesUsed: number): 'retry' | 'give-up' {
  return retriesUsed < MAX_AUTO_RETRIES ? 'retry' : 'give-up';
}

/** Text shown instead of the relay-bound frame, which is not a ready terminal. */
export const RELAY_BOUND_MESSAGE =
  '\x1b[36m[*] Channel established, waiting for Arthas interactive session...\x1b[0m\r\n';
