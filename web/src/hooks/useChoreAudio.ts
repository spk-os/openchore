import { useCallback, useEffect, useRef, useState } from 'react';

/**
 * One shared <audio> element for chore read-aloud clips. Keeps a single
 * playback going at a time (a new tap replaces the old clip) and surfaces
 * whether something is playing — the say button shows it, so a tap is
 * never a silent no-op (a short clip or a muted phone used to read as
 * "nothing happened").
 *
 * Falls back to the caller's speech() when the clip cannot play at all
 * (missing file, blocked autoplay, unsupported media).
 */
export function useChoreAudio(onFallback: (text: string) => void) {
  const audioRef = useRef<HTMLAudioElement | null>(null);
  const [speakingId, setSpeakingId] = useState<string | null>(null);
  const fallbackRef = useRef(onFallback);
  fallbackRef.current = onFallback;

  useEffect(() => {
    return () => { audioRef.current?.pause(); };
  }, []);

  /** Play a clip (or fall back to browser speech). key identifies the row. */
  const speak = useCallback((key: string, text: string, audioUrl?: string) => {
    // A tap on the row already speaking stops it (toggle semantics).
    if (audioRef.current && !audioRef.current.paused && speakingId === key) {
      audioRef.current.pause();
      setSpeakingId(null);
      return;
    }
    if (!audioUrl) {
      // Browser speech has no completion signal; still flip the button so
      // the tap visibly did something, then clear when utterance ends.
      setSpeakingId(key);
      fallbackRef.current(text);
      window.setTimeout(() => setSpeakingId(cur => (cur === key ? null : cur)), 800);
      return;
    }
    const audio = audioRef.current ?? new Audio();
    audioRef.current = audio;
    audio.src = audioUrl;
    audio.onended = () => setSpeakingId(null);
    audio.onerror = () => {
      setSpeakingId(null);
      fallbackRef.current(text);
    };
    setSpeakingId(key);
    audio.play().catch(() => {
      // Autoplay policy or decode failure: hand it to browser speech.
      setSpeakingId(null);
      fallbackRef.current(text);
    });
  }, [speakingId]);

  const stop = useCallback(() => {
    audioRef.current?.pause();
    setSpeakingId(null);
  }, []);

  return { speakingId, speak, stop };
}
