// @vitest-environment jsdom
// 036: the in-page recorder behind PhotoSheet's 直接录音/直接录像. Mocked
// getUserMedia + MediaRecorder: start/stop produce a typed File, cancel
// throws the partial take away, permission denial surfaces 'denied', and
// unmount tears the stream down (the mic indicator must not linger).
import { describe, it, expect, vi, beforeAll, afterAll, afterEach } from 'vitest';
import { renderHook, act, waitFor } from '@testing-library/react';
import i18n from '../../i18n';
import { useRecorder } from './useRecorder';

// A fake track/stream pair; stop() is the observable side effect.
const makeTrack = () => ({ stop: vi.fn() });
const makeStream = (tracks: ReturnType<typeof makeTrack>[]) => ({
  getTracks: () => tracks,
});

// A MediaRecorder double: ondataavailable fires on start(timeslice) via
// the chunks the test pushes, onstop fires on stop().
class FakeRecorder {
  static instances: FakeRecorder[] = [];
  mimeType: string;
  state: 'inactive' | 'recording' = 'recording';
  ondataavailable: ((e: { data: Blob }) => void) | null = null;
  onstop: (() => void) | null = null;
  private chunks: Blob[] = [];

  constructor(_stream: unknown, opts?: { mimeType?: string }) {
    this.mimeType = opts?.mimeType ?? 'audio/webm;codecs=opus';
    FakeRecorder.instances.push(this);
  }
  static isTypeSupported(t: string) { return t.startsWith('audio/webm') || t.startsWith('video/webm'); }
  pushChunk(data: Blob) { this.chunks.push(data); this.ondataavailable?.({ data }); }
  start() { this.state = 'recording'; }
  stop() {
    if (this.state === 'inactive') return;
    this.state = 'inactive';
    this.onstop?.();
  }
}

describe('useRecorder', () => {
  let getUserMedia: ReturnType<typeof vi.fn>;
  const RealMediaRecorder = globalThis.MediaRecorder;

  beforeAll(async () => {
    await i18n.changeLanguage('en');
    FakeRecorder.instances = [];
    vi.stubGlobal('MediaRecorder', FakeRecorder as unknown as typeof MediaRecorder);
    getUserMedia = vi.fn();
    vi.stubGlobal('navigator', { ...navigator, mediaDevices: { getUserMedia } });
  });
  afterAll(() => {
    vi.unstubAllGlobals();
    Object.defineProperty(globalThis, 'MediaRecorder', { value: RealMediaRecorder, configurable: true });
  });
  afterEach(() => { FakeRecorder.instances = []; getUserMedia.mockReset(); });

  it('records audio and resolves stop() with a typed File', async () => {
    const tracks = [makeTrack()];
    getUserMedia.mockResolvedValue(makeStream(tracks));

    const { result } = renderHook(() => useRecorder());
    await act(async () => { await result.current.start('audio'); });
    expect(result.current.state).toBe('recording');
    expect(FakeRecorder.instances.length).toBe(1);

    // Push a chunk like the timeslice callback would.
    act(() => { FakeRecorder.instances[0].pushChunk(new Blob(['opus-bytes'], { type: 'audio/webm' })); });

    let filePromise: Promise<File | null> | undefined;
    await act(async () => { filePromise = result.current.stop(); });
    const file = await filePromise!;
    expect(file).toBeTruthy();
    expect(file!.type.startsWith('audio/')).toBe(true);
    expect(file!.name).toMatch(/^rec-.*\.webm$/);
    expect(result.current.state).toBe('idle');
    // The mic is released as soon as the take completes.
    expect(tracks[0].stop).toHaveBeenCalled();
  });

  it('cancel discards a pending take and releases the stream', async () => {
    const tracks = [makeTrack()];
    getUserMedia.mockResolvedValue(makeStream(tracks));

    const { result } = renderHook(() => useRecorder());
    await act(async () => { await result.current.start('audio'); });

    // Cancel mid-take (no stop() pending): cancel detaches onstop (the
    // File is thrown away), stops the recorder, tears the stream down
    // and returns the state to idle.
    const recorder = FakeRecorder.instances[0];
    act(() => { result.current.cancel(); });
    expect(recorder.state).toBe('inactive');   // cancel stopped it
    expect(recorder.onstop).toBeNull();        // the File callback is detached
    expect(result.current.state).toBe('idle');
    expect(tracks[0].stop).toHaveBeenCalled();
  });

  it('surfaces permission denial as state "denied"', async () => {
    getUserMedia.mockRejectedValue(new DOMException('denied', 'NotAllowedError'));

    const { result } = renderHook(() => useRecorder());
    await act(async () => { await result.current.start('audio'); });
    expect(result.current.state).toBe('denied');
    expect(result.current.error).toBeTruthy();
  });

  it('releases the stream on unmount mid-take', async () => {
    const tracks = [makeTrack()];
    getUserMedia.mockResolvedValue(makeStream(tracks));

    const { result, unmount } = renderHook(() => useRecorder());
    await act(async () => { await result.current.start('audio'); });
    unmount();
    // The teardown effect stops the tracks even though the take is lost.
    await waitFor(() => expect(tracks[0].stop).toHaveBeenCalled());
  });
});
