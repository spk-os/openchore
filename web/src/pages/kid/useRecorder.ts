import { useCallback, useEffect, useRef, useState } from 'react';
import { useTranslation } from 'react-i18next';

// 036: the in-page recorder behind PhotoSheet's 「直接录音 / 直接录像」
// buttons. getUserMedia + MediaRecorder — no file picker hop: the kid taps
// record, performs (reads the poem, plays the piece), taps stop, and the
// blob lands in the attachment queue like a picked file.
//
// Safari quirks shape the implementation:
// - MediaRecorder.isTypeSupported gates the codec choice; webm/opus is the
//   Chrome/Firefox default, mp4 (really m4a/aac) is Safari's, and the
//   fallback is whatever the browser offers (server sniffs anyway).
// - The stream MUST be stopped on unmount, or the mic/camera indicator
//   stays on after the sheet closes.

/** Every codec pair we would accept, best first. */
const AUDIO_TYPES = ['audio/webm;codecs=opus', 'audio/webm', 'audio/mp4', 'audio/ogg;codecs=opus', ''];
const VIDEO_TYPES = ['video/webm;codecs=vp9,opus', 'video/webm;codecs=vp8,opus', 'video/webm', 'video/mp4', ''];

const EXT_FOR_TYPE: Record<string, string> = {
  'audio/webm': 'webm', 'audio/ogg': 'ogg', 'audio/mp4': 'm4a',
  'video/webm': 'webm', 'video/mp4': 'mp4',
};

function pickMime(candidates: string[]): string {
  if (typeof MediaRecorder === 'undefined') return '';
  for (const t of candidates) {
    if (t === '') return t; // the browser default always works
    if (MediaRecorder.isTypeSupported(t)) return t;
  }
  return '';
}

export type RecorderKind = 'audio' | 'video';

interface UseRecorderResult {
  /** 'idle' | 'recording' | 'stopping'; 'denied' means permission refused. */
  state: 'idle' | 'recording' | 'stopping' | 'denied' | 'unsupported';
  /** Seconds since recording started (drives the live timer). */
  seconds: number;
  /** A terminal failure the sheet shows as an upload error line. */
  error: string | null;
  start: (kind: RecorderKind) => Promise<void>;
  /** Resolves with the finished blob as a File, or null when cancelled. */
  stop: () => Promise<File | null>;
  /** Cancel: stop the stream and throw the partial take away. */
  cancel: () => void;
}

export function useRecorder(): UseRecorderResult {
  const { t } = useTranslation();
  const [state, setState] = useState<UseRecorderResult['state']>('idle');
  const [seconds, setSeconds] = useState(0);
  const [error, setError] = useState<string | null>(null);
  const recorderRef = useRef<MediaRecorder | null>(null);
  const chunksRef = useRef<BlobPart[]>([]);
  const streamRef = useRef<MediaStream | null>(null);
  const kindRef = useRef<RecorderKind>('audio');
  const timerRef = useRef<number | null>(null);
  // The pending stop promise: stop() must wait for onstop (the blob is
  // only complete then), so the resolver travels through the ref.
  const resolveRef = useRef<((f: File | null) => void) | null>(null);

  const teardown = useCallback(() => {
    if (timerRef.current != null) {
      window.clearInterval(timerRef.current);
      timerRef.current = null;
    }
    recorderRef.current = null;
    chunksRef.current = [];
    if (streamRef.current) {
      for (const track of streamRef.current.getTracks()) track.stop();
      streamRef.current = null;
    }
  }, []);

  // Leaving the sheet mid-take must release the mic/camera.
  useEffect(() => () => teardown(), [teardown]);

  const start = useCallback(async (kind: RecorderKind) => {
    setError(null);
    if (typeof MediaRecorder === 'undefined' || !navigator.mediaDevices?.getUserMedia) {
      setState('unsupported');
      setError(t('kid.photo.recUnsupported'));
      return;
    }
    try {
      const stream = kind === 'audio'
        ? await navigator.mediaDevices.getUserMedia({ audio: true })
        : await navigator.mediaDevices.getUserMedia({ audio: true, video: { facingMode: 'user', width: 640, height: 480 } });
      streamRef.current = stream;
      kindRef.current = kind;
      const mimeType = pickMime(kind === 'audio' ? AUDIO_TYPES : VIDEO_TYPES);
      const recorder = mimeType !== ''
        ? new MediaRecorder(stream, { mimeType })
        : new MediaRecorder(stream);
      chunksRef.current = [];
      recorder.ondataavailable = (e) => {
        if (e.data.size > 0) chunksRef.current.push(e.data);
      };
      recorder.onstop = () => {
        const type = recorder.mimeType || (kindRef.current === 'audio' ? 'audio/webm' : 'video/webm');
        const blob = new Blob(chunksRef.current, { type });
        const ext = EXT_FOR_TYPE[type.split(';')[0]] ?? (kindRef.current === 'audio' ? 'webm' : 'webm');
        const stamp = new Date().toISOString().replace(/[:.]/g, '-').slice(0, 19);
        const file = new File([blob], `${kindRef.current === 'audio' ? 'rec' : 'video'}-${stamp}.${ext}`, { type });
        teardown();
        setSeconds(0);
        setState('idle');
        resolveRef.current?.(file);
        resolveRef.current = null;
      };
      recorder.start(500); // timeslice: a crash loses at most the last chunk
      recorderRef.current = recorder;
      setSeconds(0);
      setState('recording');
      timerRef.current = window.setInterval(() => setSeconds(s => s + 1), 1000);
    } catch (e) {
      teardown();
      // NotAllowedError is the permission refusal; anything else (no
      // device, busy) reads the same to the kid: "can't record now".
      setState(e instanceof DOMException && e.name === 'NotAllowedError' ? 'denied' : 'unsupported');
      setError(t('kid.photo.recDenied'));
    }
  }, [t, teardown]);

  const stop = useCallback(async () => {
    const recorder = recorderRef.current;
    if (!recorder || recorder.state === 'inactive') return null;
    setState('stopping');
    const file = await new Promise<File | null>(resolve => {
      resolveRef.current = resolve;
      recorder.stop();
    });
    return file;
  }, []);

  const cancel = useCallback(() => {
    const recorder = recorderRef.current;
    resolveRef.current = null;
    if (recorder && recorder.state !== 'inactive') {
      recorder.onstop = null;
      recorder.stop();
    }
    teardown();
    setSeconds(0);
    setState('idle');
  }, [teardown]);

  return { state, seconds, error, start, stop, cancel };
}
