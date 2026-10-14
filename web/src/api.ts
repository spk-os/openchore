import type { User, PersonColor, AuthSession, AuthProvider, LinkedIdentity, ScheduledChore, Chore, ChoreSchedule, PointsData, PointBalance, PendingCompletion, Reward, RewardRedemption, RewardCommitment, SharedCommitmentPool, RedemptionHistory, UserStreakData, StreakRewardItem, ChoreTrigger, Webhook, WebhookDelivery, UserDecayConfig, APIToken, AIStatus, AIReviewResult, AIConfig, AIConnectionUpdate, AdminAuthConfig, AuthProviderInput, SignInOptions, DailyCoachNote, ChoreTemplate, TemplateTune, TemplateImportRequest, TemplateImportResult, CopilotDraftResponse, CopilotApplyTask, CopilotApplyResult, Pet, PetResponse, BoostLink, BoostSnapshot, CompletionAttachment, TaskTemplate, TaskTemplateWithUsage, TaskTemplateInstance, ErrorItem, PaperAnalysis, ErrorBookReport, ReviewItem, ReviewToday, FSRSParams, ReviewStats, Course, CourseTree, CourseUnit, UnitResource, AdminCourseTree, CourseOutcome, OutcomeView, OutcomeAssessment, ProgressReport, ProgressResponse } from './types';

const API_BASE = '/api';

// Resize images client-side before uploading to avoid Nginx body size limits
// and reduce upload time on mobile. Returns a JPEG file ≤ maxDim on longest side.
function resizeImage(file: File, maxDim: number): Promise<File> {
  return new Promise((resolve) => {
    // Skip if already small enough (< 500KB)
    if (file.size < 500_000) {
      resolve(file);
      return;
    }
    const img = new Image();
    img.onload = () => {
      URL.revokeObjectURL(img.src);
      const { width, height } = img;
      if (width <= maxDim && height <= maxDim) {
        resolve(file);
        return;
      }
      const scale = maxDim / Math.max(width, height);
      const canvas = document.createElement('canvas');
      canvas.width = Math.round(width * scale);
      canvas.height = Math.round(height * scale);
      const ctx = canvas.getContext('2d')!;
      ctx.drawImage(img, 0, 0, canvas.width, canvas.height);
      canvas.toBlob(
        (blob) => {
          resolve(new File([blob!], file.name.replace(/\.\w+$/, '.jpg'), { type: 'image/jpeg' }));
        },
        'image/jpeg',
        0.85,
      );
    };
    img.onerror = () => resolve(file); // fallback to original on error
    img.src = URL.createObjectURL(file);
  });
}

export class APIError extends Error {
  status: number;
  data: any;
  constructor(message: string, status: number, data: any) {
    super(message);
    this.name = 'APIError';
    this.status = status;
    this.data = data;
  }
}

// Sessions ride on an HttpOnly cookie set by the server, so requests carry
// no identity headers. The one exception is the photo-upload page opened from
// a QR code on another device: it authenticates with a short-lived,
// single-chore token passed in the link.
let bearerOverride: string | null = null;
export function setBearerToken(token: string | null) {
  bearerOverride = token;
}

// Fired when the server says the session is gone (expired, revoked, signed
// out elsewhere) so the app can return to the profile picker.
export const SESSION_EXPIRED_EVENT = 'openchore:session-expired';

async function fetchWithAuth<T>(path: string, options: RequestInit = {}, skipContentType = false): Promise<T> {
  const headers: Record<string, string> = {
    ...(skipContentType ? {} : { 'Content-Type': 'application/json' }),
    ...(bearerOverride ? { Authorization: `Bearer ${bearerOverride}` } : {}),
    ...options.headers as Record<string, string>,
  };

  const resp = await fetch(`${API_BASE}${path}`, { ...options, headers, credentials: 'same-origin' });
  if (resp.status === 401 && !bearerOverride) {
    window.dispatchEvent(new Event(SESSION_EXPIRED_EVENT));
  }
  if (!resp.ok) {
    const err = await resp.json().catch(() => ({ error: 'Unknown error' }));
    throw new APIError(err.error || `HTTP error! status: ${resp.status}`, resp.status, err);
  }
  if (resp.status === 204) return {} as T;
  return resp.json();
}

async function fetchPublic<T>(path: string, options: RequestInit = {}): Promise<T> {
  const headers: Record<string, string> = {
    'Content-Type': 'application/json',
    ...options.headers as Record<string, string>,
  };
  const resp = await fetch(`${API_BASE}${path}`, { ...options, headers, credentials: 'same-origin' });
  if (!resp.ok) {
    const err = await resp.json().catch(() => ({ error: 'Unknown error' }));
    throw new APIError(err.error || `HTTP error! status: ${resp.status}`, resp.status, err);
  }
  if (resp.status === 204) return {} as T;
  return resp.json();
}

// Public, read-only per-user data (chores, points, streak) for the wall
// display, which shows everyone without anyone signing in.
export function fetchPublicUserData<T>(path: string): Promise<T> {
  return fetchPublic<T>(path);
}

export interface LoginRequest {
  user_id: number;
  pin?: string;
  legacy_passcode?: string;
  new_pin?: string;
}

export const api = {
  auth: {
    login: (req: LoginRequest) =>
      fetchPublic<AuthSession>('/auth/login', { method: 'POST', body: JSON.stringify(req) }),
    logout: () => fetchPublic('/auth/logout', { method: 'POST' }),
    logoutEverywhere: () => fetchWithAuth('/auth/logout-everywhere', { method: 'POST' }),
    me: () => fetchPublic<AuthSession>('/auth/me'),
    providers: () => fetchPublic<AuthProvider[]>('/auth/providers'),
    options: () => fetchPublic<SignInOptions>('/auth/options'),
    uploadLink: (scheduleId: number) =>
      fetchWithAuth<{ token: string; expires_at: string }>('/auth/upload-link', {
        method: 'POST',
        body: JSON.stringify({ schedule_id: scheduleId }),
      }),
    // Full-page navigations: the provider redirects back to the app.
    oidcLoginURL: (providerId: string, userId?: number, returnPath?: string) =>
      `${API_BASE}/auth/oidc/${encodeURIComponent(providerId)}/start?mode=login` +
      (userId ? `&user_id=${userId}` : '') +
      (returnPath ? `&return=${encodeURIComponent(returnPath)}` : ''),
    oidcLinkURL: (providerId: string, returnPath: string) =>
      `${API_BASE}/auth/oidc/${encodeURIComponent(providerId)}/start?mode=link&return=${encodeURIComponent(returnPath)}`,
    identities: (userId: number) => fetchWithAuth<LinkedIdentity[]>(`/users/${userId}/identities`),
    unlink: (userId: number, identityId: number) =>
      fetchWithAuth(`/users/${userId}/identities/${identityId}`, { method: 'DELETE' }),
  },
  users: {
    list: () => fetchPublic<User[]>('/users'),
    get: (id: number) => fetchPublic<User>(`/users/${id}`),
    create: (data: Partial<User>) => fetchWithAuth<User>('/users', {
      method: 'POST',
      body: JSON.stringify(data),
    }),
    update: (id: number, data: Partial<User>) => fetchWithAuth<User>(`/users/${id}`, {
      method: 'PUT',
      body: JSON.stringify(data),
    }),
    delete: (id: number) => fetchWithAuth(`/users/${id}`, { method: 'DELETE' }),
    updateTheme: (id: number, theme: string) =>
      fetchWithAuth<User>(`/users/${id}/theme`, {
        method: 'PUT',
        body: JSON.stringify({ theme }),
      }),
    updateAvatar: (id: number, avatar_url: string) =>
      fetchWithAuth<User>(`/users/${id}/avatar`, {
        method: 'PUT',
        body: JSON.stringify({ avatar_url }),
      }),
    updateLineColor: (id: number, line_color: string) =>
      fetchWithAuth<User>(`/users/${id}/line-color`, {
        method: 'PUT',
        body: JSON.stringify({ line_color }),
      }),
    updateColor: (id: number, color: PersonColor) =>
      fetchWithAuth<User>(`/users/${id}/color`, {
        method: 'PUT',
        body: JSON.stringify({ color }),
      }),
    setPin: (id: number, newPin: string, currentPin?: string) =>
      fetchWithAuth<{ has_pin: boolean; pin_length: number }>(`/users/${id}/pin`, {
        method: 'PUT',
        body: JSON.stringify({ new_pin: newPin, current_pin: currentPin ?? '' }),
      }),
    clearPin: (id: number, currentPin?: string) =>
      fetchWithAuth<{ has_pin: boolean }>(`/users/${id}/pin`, {
        method: 'DELETE',
        body: JSON.stringify({ current_pin: currentPin ?? '' }),
      }),
    pause: (id: number) =>
      fetchWithAuth<User>(`/users/${id}/pause`, { method: 'PUT' }),
    unpause: (id: number) =>
      fetchWithAuth<User>(`/users/${id}/unpause`, { method: 'PUT' }),
    getChores: (id: number, view: 'daily' | 'weekly', date: string) =>
      fetchWithAuth<ScheduledChore[]>(`/users/${id}/chores?view=${view}&date=${date}`),
  },
  chores: {
    list: () => fetchWithAuth<Chore[]>('/chores'),
    get: (id: number) => fetchWithAuth<Chore>(`/chores/${id}`),
    create: (data: Partial<Chore>) => fetchWithAuth<Chore>('/chores', {
      method: 'POST',
      body: JSON.stringify(data),
    }),
    update: (id: number, data: Partial<Chore>) => fetchWithAuth<Chore>(`/chores/${id}`, {
      method: 'PUT',
      body: JSON.stringify(data),
    }),
    delete: (id: number) => fetchWithAuth(`/chores/${id}`, { method: 'DELETE' }),
    // The response carries the completion's id (for attaching more proof
    // through the multi-file channel) plus the surprise bonus drop.
    complete: (scheduleId: number, date: string, photoUrl?: string, opts?: { skipPhoto?: boolean }) =>
      fetchWithAuth<{ id?: number; status?: string; bonus_drop?: number }>(`/schedules/${scheduleId}/complete`, {
        method: 'POST',
        body: JSON.stringify({ completion_date: date, photo_url: photoUrl, skip_photo: opts?.skipPhoto || undefined }),
      }),
    uncomplete: (scheduleId: number, date: string) =>
      fetchWithAuth(`/schedules/${scheduleId}/complete?date=${date}`, {
        method: 'DELETE',
      }),
    upload: async (file: File) => {
      // Only photos are downscaled; videos and documents go up untouched
      // (resizing a video in a canvas isn't a thing, and shrinking a PDF
      // would ruin it).
      const resized = file.type.startsWith('image/') ? await resizeImage(file, 1280) : file;
      const formData = new FormData();
      formData.append('photo', resized);
      return fetchWithAuth<{ url: string }>('/upload', {
        method: 'POST',
        body: formData,
      }, true);
    },
    attachPhoto: (completionId: number, photoUrl: string) =>
      fetchWithAuth(`/completions/${completionId}/photo`, {
        method: 'PUT',
        body: JSON.stringify({ photo_url: photoUrl }),
      }),
    // L0 multi-file proof: several photos (up to 9), one video or one PDF
    // travel in one request. Duplicate files come back skipped, not errors.
    uploadAttachments: async (completionId: number, files: File[]) => {
      // Downscale photos client-side (same reasons as upload()); videos
      // and PDFs go up untouched.
      const resized = await Promise.all(files.map(f =>
        f.type.startsWith('image/') ? resizeImage(f, 1280) : Promise.resolve(f)));
      const formData = new FormData();
      for (const f of resized) formData.append('files[]', f);
      return fetchWithAuth<{ added: CompletionAttachment[]; skipped: number; count: number }>(
        `/completions/${completionId}/attachments`,
        { method: 'POST', body: formData },
        true,
      );
    },
    listAttachments: (completionId: number) =>
      fetchWithAuth<CompletionAttachment[]>(`/completions/${completionId}/attachments`),
    deleteAttachment: (attachmentId: number) =>
      fetchWithAuth(`/admin/attachments/${attachmentId}`, { method: 'DELETE' }),
    listPending: () => fetchWithAuth<PendingCompletion[]>('/completions/pending'),
    approve: (completionId: number) => fetchWithAuth(`/completions/${completionId}/approve`, { method: 'POST' }),
    reject: (completionId: number) => fetchWithAuth(`/completions/${completionId}/reject`, { method: 'POST' }),
    excuse: (scheduleId: number, date: string, reason?: string) =>
      fetchWithAuth(`/schedules/${scheduleId}/excuse`, {
        method: 'POST',
        body: JSON.stringify({ date, reason: reason ?? '' }),
      }),
    listSchedules: (choreId: number) =>
      fetchWithAuth<ChoreSchedule[]>(`/chores/${choreId}/schedules`),
    createSchedule: (choreId: number, data: Partial<ChoreSchedule>) =>
      fetchWithAuth<ChoreSchedule>(`/chores/${choreId}/schedules`, {
        method: 'POST',
        body: JSON.stringify(data),
      }),
    deleteSchedule: (choreId: number, scheduleId: number) =>
      fetchWithAuth(`/chores/${choreId}/schedules/${scheduleId}`, { method: 'DELETE' }),
    regenerateTTS: (choreId: number) =>
      fetchWithAuth<{ tts_audio_url: string }>(`/chores/${choreId}/tts/regenerate`, { method: 'POST' }),
  },
  points: {
    getForUser: (userId: number) => fetchWithAuth<PointsData>(`/users/${userId}/points`),
    getAllBalances: () => fetchWithAuth<PointBalance[]>('/points/balances'),
    adjust: (userId: number, amount: number, note: string) =>
      fetchWithAuth('/points/adjust', {
        method: 'POST',
        body: JSON.stringify({ user_id: userId, amount, note }),
      }),
  },
  rewards: {
    list: () => fetchWithAuth<Reward[]>('/rewards'),
    listAll: () => fetchWithAuth<Reward[]>('/rewards/all'),
    create: (data: Partial<Reward>) => fetchWithAuth<Reward>('/rewards', {
      method: 'POST',
      body: JSON.stringify(data),
    }),
    update: (id: number, data: Partial<Reward>) => fetchWithAuth<Reward>(`/rewards/${id}`, {
      method: 'PUT',
      body: JSON.stringify(data),
    }),
    delete: (id: number) => fetchWithAuth(`/rewards/${id}`, { method: 'DELETE' }),
    setAssignments: (id: number, assignments: { user_id: number; custom_cost?: number }[]) =>
      fetchWithAuth(`/rewards/${id}/assignments`, {
        method: 'PUT',
        body: JSON.stringify({ assignments }),
      }),
    redeem: (id: number) => fetchWithAuth<RewardRedemption>(`/rewards/${id}/redeem`, { method: 'POST' }),
    listRedemptions: (userId: number) => fetchWithAuth<RedemptionHistory[]>(`/users/${userId}/redemptions`),
    undoRedemption: (redemptionId: number) => fetchWithAuth(`/redemptions/${redemptionId}`, { method: 'DELETE' }),
  },
  commitments: {
    listForUser: (userId: number) => fetchWithAuth<RewardCommitment[]>(`/users/${userId}/commitments`),
    commit: (rewardId: number, autoContributePercent: number) =>
      fetchWithAuth<RewardCommitment>(`/rewards/${rewardId}/commit`, {
        method: 'POST',
        body: JSON.stringify({ auto_contribute_percent: autoContributePercent }),
      }),
    contribute: (commitmentId: number, amount: number) =>
      fetchWithAuth<RewardCommitment>(`/commitments/${commitmentId}/contribute`, {
        method: 'POST',
        body: JSON.stringify({ amount }),
      }),
    setAutoContribute: (commitmentId: number, percent: number) =>
      fetchWithAuth<RewardCommitment>(`/commitments/${commitmentId}/auto-contribute`, {
        method: 'PUT',
        body: JSON.stringify({ percent }),
      }),
    break: (commitmentId: number) =>
      fetchWithAuth(`/commitments/${commitmentId}`, { method: 'DELETE' }),
    getPool: (poolId: number) => fetchWithAuth<SharedCommitmentPool>(`/pools/${poolId}`),
  },
  streaks: {
    getForUser: (userId: number) => fetchWithAuth<UserStreakData>(`/users/${userId}/streak`),
    listRewards: () => fetchWithAuth<StreakRewardItem[]>('/admin/streak-rewards'),
    createReward: (data: Partial<StreakRewardItem>) =>
      fetchWithAuth<StreakRewardItem>('/admin/streak-rewards', {
        method: 'POST',
        body: JSON.stringify(data),
      }),
    deleteReward: (id: number) => fetchWithAuth(`/admin/streak-rewards/${id}`, { method: 'DELETE' }),
  },
  coach: {
    // The daily coaching note; date defaults to today on the server.
    getNote: (userId: number, date?: string) =>
      fetchWithAuth<DailyCoachNote>(`/users/${userId}/coach-note${date ? `?date=${date}` : ''}`),
  },
  pet: {
    // The kid's pet (P4-13). 404 means the feature is off — hide the layer.
    get: () => fetchWithAuth<PetResponse>('/pet'),
    hatch: (name: string) => fetchWithAuth<Pet>('/pet/hatch', {
      method: 'POST',
      body: JSON.stringify({ name }),
    }),
    feed: () => fetchWithAuth<Pet>('/pet/feed', { method: 'POST' }),
    // Admin view of any kid's pet (the parent status card). 404 hides it.
    getForAdmin: (userId: number) => fetchWithAuth<PetResponse>(`/admin/users/${userId}/pet`),
  },
  boost: {
    // The elder's boost page (P4-16): public, token-addressed, no session.
    page: (token: string) => fetchPublic<BoostSnapshot>(`/boost/${encodeURIComponent(token)}`),
    send: (token: string, amount: number) =>
      fetchPublic<BoostSnapshot>(`/boost/${encodeURIComponent(token)}`, {
        method: 'POST',
        body: JSON.stringify({ amount }),
      }),
  },
  decay: {
    getConfig: (userId: number) => fetchWithAuth<UserDecayConfig>(`/admin/users/${userId}/decay`),
    setConfig: (userId: number, data: Partial<UserDecayConfig>) =>
      fetchWithAuth<UserDecayConfig>(`/admin/users/${userId}/decay`, {
        method: 'PUT',
        body: JSON.stringify(data),
      }),
  },
  reports: {
    get: (period: string, date: string) =>
      fetchWithAuth<any>(`/admin/reports?period=${period}&date=${date}`),
  },
  admin: {
    getSetting: (key: string) => fetchWithAuth<{ key: string; value: string }>(`/admin/settings/${key}`),
    setSetting: (key: string, value: string) => fetchWithAuth<{ key: string; value: string }>(`/admin/settings/${key}`, {
      method: 'PUT',
      body: JSON.stringify({ value }),
    }),
    aiStatus: () => fetchWithAuth<AIStatus>('/admin/ai/status'),
    aiConfig: () => fetchWithAuth<AIConfig>('/admin/ai/config'),
    updateAIConfig: (data: { ai?: AIConnectionUpdate; tts?: AIConnectionUpdate }) =>
      fetchWithAuth<AIConfig>('/admin/ai/config', { method: 'PUT', body: JSON.stringify(data) }),
    authConfig: () => fetchWithAuth<AdminAuthConfig>('/admin/auth/config'),
    updateSessionLengths: (data: { kiosk_session_hours: number | null; personal_session_hours: number | null }) =>
      fetchWithAuth<AdminAuthConfig>('/admin/auth/sessions', { method: 'PUT', body: JSON.stringify(data) }),
    updateSignInOptions: (data: { pin_sign_in?: boolean; start_page?: string }) =>
      fetchWithAuth<AdminAuthConfig>('/admin/auth/options', { method: 'PUT', body: JSON.stringify(data) }),
    createAuthProvider: (data: AuthProviderInput) =>
      fetchWithAuth<AdminAuthConfig>('/admin/auth/providers', { method: 'POST', body: JSON.stringify(data) }),
    updateAuthProvider: (id: string, data: AuthProviderInput) =>
      fetchWithAuth<AdminAuthConfig>(`/admin/auth/providers/${encodeURIComponent(id)}`, { method: 'PUT', body: JSON.stringify(data) }),
    deleteAuthProvider: (id: string) =>
      fetchWithAuth<AdminAuthConfig>(`/admin/auth/providers/${encodeURIComponent(id)}`, { method: 'DELETE' }),
    testAuthProvider: (id: string) =>
      fetchWithAuth<{ status: string }>(`/admin/auth/providers/${encodeURIComponent(id)}/test`, { method: 'POST' }),
    testAIReview: (choreTitle: string, photoUrl: string) =>
      fetchWithAuth<AIReviewResult>('/admin/ai/test', {
        method: 'POST',
        body: JSON.stringify({ chore_title: choreTitle, photo_url: photoUrl }),
      }),
    generateDescription: (title: string, category: string) =>
      fetchWithAuth<{ description: string }>('/admin/ai/generate-description', {
        method: 'POST',
        body: JSON.stringify({ title, category }),
      }),
    listTemplates: () => fetchWithAuth<ChoreTemplate[]>('/admin/templates'),
    // L0 task templates: a saved chore (Habitica-style), instantiated and
    // pushed to not-yet-done instances. Distinct from the built-in packs
    // above (/admin/templates).
    listTaskTemplates: (includeArchived?: boolean) =>
      fetchWithAuth<TaskTemplateWithUsage[]>(`/admin/task-templates${includeArchived ? '?archived=1' : ''}`),
    createTaskTemplate: (data: Partial<TaskTemplate> & { from_chore_id?: number }) =>
      fetchWithAuth<TaskTemplate>('/admin/task-templates', {
        method: 'POST',
        body: JSON.stringify(data),
      }),
    updateTaskTemplate: (id: number, data: Partial<TaskTemplate>) =>
      fetchWithAuth<TaskTemplate>(`/admin/task-templates/${id}`, {
        method: 'PUT',
        body: JSON.stringify(data),
      }),
    archiveTaskTemplate: (id: number) =>
      fetchWithAuth(`/admin/task-templates/${id}`, { method: 'DELETE' }),
    instantiateTaskTemplate: (id: number, data: { assigned_to: number; specific_date?: string; available_at?: string; due_by?: string }) =>
      fetchWithAuth<TaskTemplateInstance>(`/admin/task-templates/${id}/instantiate`, {
        method: 'POST',
        body: JSON.stringify(data),
      }),
    syncTaskTemplate: (id: number, instanceIds?: number[]) =>
      fetchWithAuth<{ updated: number }>(`/admin/task-templates/${id}/sync`, {
        method: 'POST',
        body: JSON.stringify({ instance_ids: instanceIds ?? [] }),
      }),
    tuneTemplate: (templateId: string, tasks: { title: string; category: string; points: number; estimated_minutes?: number }[], tweak: string) =>
      fetchWithAuth<TemplateTune>(`/admin/templates/${encodeURIComponent(templateId)}/tune`, {
        method: 'POST',
        body: JSON.stringify({ tasks, tweak }),
      }),
    importTemplate: (req: TemplateImportRequest) =>
      fetchWithAuth<TemplateImportResult>('/admin/templates/import', {
        method: 'POST',
        body: JSON.stringify(req),
      }),
    copilotDraft: (req: { assigned_to: number; mode?: 'text' | 'flyer'; request?: string; photo_url?: string }) =>
      fetchWithAuth<CopilotDraftResponse>('/admin/copilot/draft', {
        method: 'POST',
        body: JSON.stringify(req),
      }),
    copilotApply: (req: { assigned_to: number; source: 'copilot' | 'flyer'; tasks: CopilotApplyTask[] }) =>
      fetchWithAuth<CopilotApplyResult>('/admin/copilot/apply', {
        method: 'POST',
        body: JSON.stringify(req),
      }),
    // L1 whole-paper parse: start the async job, poll it, then act on the
    // extracted wrong questions.
    startPaperAnalysis: (completionId: number, subject?: string) =>
      fetchWithAuth<PaperAnalysis>('/admin/analyses', {
        method: 'POST',
        body: JSON.stringify({ completion_id: completionId, subject }),
      }),
    getPaperAnalysis: (id: number) => fetchWithAuth<PaperAnalysis>(`/admin/analyses/${id}`),
    errorBookReport: (userId: number) =>
      fetchWithAuth<ErrorBookReport>(`/admin/users/${userId}/error-book-report`),
    createCorrectionTask: (errorItemId: number, points?: number, specificDate?: string, dueBy?: string) =>
      fetchWithAuth<{ chore: Chore; schedule: ChoreSchedule }>(`/admin/error-items/${errorItemId}/correction-task`, {
        method: 'POST',
        body: JSON.stringify({ points, specific_date: specificDate, due_by: dueBy }),
      }),
    updateErrorItem: (id: number, data: Partial<ErrorItem>) =>
      fetchWithAuth<ErrorItem>(`/admin/error-items/${id}`, {
        method: 'PUT',
        body: JSON.stringify(data),
      }),
    runningDaily: (userId: number, templateId: string) =>
      fetchWithAuth<{ running: number }>(`/users/${userId}/running-daily?template_id=${encodeURIComponent(templateId)}`),
    // L4 parent side: the mastery dashboard (heatmap / 30-day load
    // forecast / streak), the FSRS dial (per kid), and per-card suspend.
    reviewStats: (userId?: number) =>
      fetchWithAuth<ReviewStats>(`/admin/review/stats${userId != null ? '?user_id=' + userId : ''}`),
    getFSRSParams: (userId: number) =>
      fetchWithAuth<FSRSParams>(`/admin/fsrs-params?user_id=${userId}`),
    updateFSRSParams: (userId: number, data: Partial<Pick<FSRSParams, 'request_retention' | 'maximum_interval' | 'enable_fuzz'>>) =>
      fetchWithAuth<FSRSParams>(`/admin/fsrs-params?user_id=${userId}`, {
        method: 'PUT',
        body: JSON.stringify(data),
      }),
    suspendReview: (id: number, suspended: boolean) =>
      fetchWithAuth<ReviewItem>(`/admin/review/${id}/suspend`, {
        method: 'POST',
        body: JSON.stringify({ suspended }),
      }),
    // L2 course authoring (Canvas Modules editor): courses are drafted,
    // filled with units (prerequisites + requirement_type + timed unlock)
    // and resources (typed + completion predicate), then published so the
    // kid sees them. Cloning rewrites prerequisites; View As Learner
    // renders the tree through the kid's own progress.
    listCourses: (userId?: number) =>
      fetchWithAuth<CourseTree[]>(`/admin/courses${userId != null ? '?user_id=' + userId : ''}`),
    createCourse: (data: { title: string; description?: string; syllabus_md?: string; user_id: number }) =>
      fetchWithAuth<Course>('/admin/courses', {
        method: 'POST',
        body: JSON.stringify(data),
      }),
    getCourse: (id: number) => fetchWithAuth<AdminCourseTree>(`/admin/courses/${id}`),
    updateCourse: (id: number, data: { title: string; description?: string; syllabus_md?: string }) =>
      fetchWithAuth<Course>(`/admin/courses/${id}`, {
        method: 'PUT',
        body: JSON.stringify(data),
      }),
    deleteCourse: (id: number) => fetchWithAuth(`/admin/courses/${id}`, { method: 'DELETE' }),
    publishCourse: (id: number) => fetchWithAuth<Course>(`/admin/courses/${id}/publish`, { method: 'POST' }),
    archiveCourse: (id: number) => fetchWithAuth<Course>(`/admin/courses/${id}/archive`, { method: 'POST' }),
    cloneCourse: (id: number, userId: number, title?: string) =>
      fetchWithAuth<Course>(`/admin/courses/${id}/clone`, {
        method: 'POST',
        body: JSON.stringify({ user_id: userId, title }),
      }),
    /** View As Learner: the tree through the kid's own progress. */
    previewCourse: (courseId: number) =>
      fetchWithAuth<CourseTree>('/admin/courses/preview', {
        method: 'POST',
        body: JSON.stringify({ course_id: courseId }),
      }),
    createUnit: (courseId: number, data: Partial<CourseUnit> & { title: string; prerequisite_unit_ids?: number[] }) =>
      fetchWithAuth<CourseUnit>(`/admin/courses/${courseId}/units`, {
        method: 'POST',
        body: JSON.stringify(data),
      }),
    updateUnit: (unitId: number, data: Partial<CourseUnit> & { title: string; prerequisite_unit_ids?: number[] }) =>
      fetchWithAuth<CourseUnit>(`/admin/units/${unitId}`, {
        method: 'PUT',
        body: JSON.stringify(data),
      }),
    deleteUnit: (unitId: number) => fetchWithAuth(`/admin/units/${unitId}`, { method: 'DELETE' }),
    createResource: (unitId: number, data: Partial<UnitResource> & { title: string; resource_type: string }) =>
      fetchWithAuth<UnitResource>(`/admin/units/${unitId}/resources`, {
        method: 'POST',
        body: JSON.stringify(data),
      }),
    updateResource: (resourceId: number, data: Partial<UnitResource>) =>
      fetchWithAuth<UnitResource>(`/admin/resources/${resourceId}`, {
        method: 'PUT',
        body: JSON.stringify(data),
      }),
    deleteResource: (resourceId: number) => fetchWithAuth(`/admin/resources/${resourceId}`, { method: 'DELETE' }),
    listOutcomes: (courseId: number) => fetchWithAuth<CourseOutcome[]>(`/admin/courses/${courseId}/outcomes`),
    createOutcome: (courseId: number, data: Partial<CourseOutcome> & { title: string; mastery_points: number; calculation_method: string }) =>
      fetchWithAuth<CourseOutcome>(`/admin/courses/${courseId}/outcomes`, {
        method: 'POST',
        body: JSON.stringify(data),
      }),
    deleteOutcome: (outcomeId: number) => fetchWithAuth(`/admin/outcomes/${outcomeId}`, { method: 'DELETE' }),
    /** The parent's manual score entry (assessment_type 'parent'). */
    createAssessment: (outcomeId: number, userId: number, score: number, comments?: string) =>
      fetchWithAuth<OutcomeAssessment>(`/admin/outcomes/${outcomeId}/assessments`, {
        method: 'POST',
        body: JSON.stringify({ user_id: userId, score, comments }),
      }),
    regenerateAllTTS: () =>
      fetchWithAuth<{ status: string }>('/admin/tts/regenerate', { method: 'POST' }),
    getAISummary: (userId: number, period: string, date: string) =>
      fetchWithAuth<{ summary: string }>(`/admin/reports/ai-summary?user_id=${userId}&period=${period}&date=${date}`),
    // Grandparent boost link management (P4-16). The token returns once
    // at creation — the listing keeps it private.
    listBoostLinks: (userId: number) =>
      fetchWithAuth<BoostLink[]>(`/admin/users/${userId}/boost-links`),
    createBoostLink: (userId: number, label: string) =>
      fetchWithAuth<BoostLink>(`/admin/users/${userId}/boost-links`, {
        method: 'POST',
        body: JSON.stringify({ label }),
      }),
    revokeBoostLink: (userId: number, linkId: number) =>
      fetchWithAuth<{ revoked: boolean }>(`/admin/users/${userId}/boost-links/${linkId}`, {
        method: 'DELETE',
      }),
    exportConfig: async (sections: string[]) => {
      const resp = await fetch(`${API_BASE}/admin/export-config?sections=${sections.join(',')}`, { credentials: 'same-origin' });
      if (!resp.ok) throw new Error('export failed');
      return resp.blob();
    },
  },
  setup: (data: { parent: { name: string; pin: string; color?: string }; children: { name: string; theme: string; color?: string }[]; chores: { title: string; icon: string; category: string; points_value: number }[] }) =>
    fetchPublic<{ admin: User; children: User[] }>('/setup', {
      method: 'POST',
      body: JSON.stringify(data),
    }),
  triggers: {
    listForChore: (choreId: number) =>
      fetchWithAuth<ChoreTrigger[]>(`/chores/${choreId}/triggers`),
    create: (choreId: number, data: Partial<ChoreTrigger>) =>
      fetchWithAuth<ChoreTrigger>(`/chores/${choreId}/triggers`, {
        method: 'POST',
        body: JSON.stringify(data),
      }),
    update: (triggerId: number, data: Partial<ChoreTrigger>) =>
      fetchWithAuth<ChoreTrigger>(`/triggers/${triggerId}`, {
        method: 'PUT',
        body: JSON.stringify(data),
      }),
    delete: (triggerId: number) =>
      fetchWithAuth(`/triggers/${triggerId}`, { method: 'DELETE' }),
  },
  // L1 error items / wrong-question loop.
  errorItems: {
    /** The kid's own book (parents pass ?user_id=). */
    list: (params?: { user_id?: number; mastery?: number; subject?: string; limit?: number }) => {
      const q = new URLSearchParams();
      if (params?.user_id != null) q.set('user_id', String(params.user_id));
      if (params?.mastery != null) q.set('mastery', String(params.mastery));
      if (params?.subject) q.set('subject', params.subject);
      if (params?.limit != null) q.set('limit', String(params.limit));
      const qs = q.toString();
      return fetchWithAuth<ErrorItem[]>(`/error-items${qs ? '?' + qs : ''}`);
    },
    get: (id: number) => fetchWithAuth<ErrorItem>(`/error-items/${id}`),
    /** The kid reports a retry outcome ("我订正完了" + parent's check). */
    submitReviewResult: (id: number, isCorrect: boolean) =>
      fetchWithAuth<ErrorItem>(`/error-items/${id}/review-result`, {
        method: 'POST',
        body: JSON.stringify({ is_correct: isCorrect }),
      }),
  },
  // L4 spaced repetition: the kid's live due queue and the two-button
  // grading (Again / Good — Anki's FAQ names the Hard-instead-of-Again
  // habit as FSRS's one uncorrectable failure mode, so those buttons
  // don't exist).
  review: {
    /** The kid's live queue ("今日复习"), the upcoming preview and the 🔥. */
    today: () => fetchWithAuth<ReviewToday>('/review/today'),
    /** Run one rating through the FSRS scheduler ("忘了"/"记住了"). */
    grade: (id: number, rating: 'Again' | 'Good') =>
      fetchWithAuth<ReviewItem>(`/review/${id}/grade`, {
        method: 'POST',
        body: JSON.stringify({ rating }),
      }),
  },
  // L2 course system, kid side (Canvas Modules/Outcomes semantics): the
  // published course list with progress, the per-course tree with each
  // unit's unlock state, the progress report that drives the unlock
  // engine, and the mastery (outcomes) rollup view.
  courses: {
    /** Published courses for this kid; drafts are invisible (edX). */
    list: () => fetchWithAuth<CourseTree[]>('/courses'),
    /** The kid's course tree: units with state/lock_explanation. */
    get: (id: number) => fetchWithAuth<CourseTree>(`/courses/${id}`),
    /** Report progress on one resource; the unit state comes back fresh. */
    reportProgress: (resourceId: number, report: ProgressReport) =>
      fetchWithAuth<ProgressResponse>(`/resources/${resourceId}/progress`, {
        method: 'POST',
        body: JSON.stringify(report),
      }),
    /** The mastery lines with this kid's rolled-up scores. */
    outcomes: (courseId: number) => fetchWithAuth<OutcomeView[]>(`/courses/${courseId}/outcomes`),
  },
  tokens: {
    list: () => fetchWithAuth<APIToken[]>('/admin/tokens'),
    create: (name: string) => fetchWithAuth<{ id: number; name: string; token: string }>('/admin/tokens', {
      method: 'POST',
      body: JSON.stringify({ name }),
    }),
    revoke: (id: number) => fetchWithAuth('/admin/tokens/' + id, { method: 'DELETE' }),
  },
  webhooks: {
    list: () => fetchWithAuth<Webhook[]>('/admin/webhooks'),
    create: (data: { url: string; secret?: string; events?: string }) =>
      fetchWithAuth<Webhook>('/admin/webhooks', {
        method: 'POST',
        body: JSON.stringify(data),
      }),
    update: (id: number, data: Partial<Webhook>) =>
      fetchWithAuth<Webhook>(`/admin/webhooks/${id}`, {
        method: 'PUT',
        body: JSON.stringify(data),
      }),
    delete: (id: number) => fetchWithAuth(`/admin/webhooks/${id}`, { method: 'DELETE' }),
    listDeliveries: (id: number) => fetchWithAuth<WebhookDelivery[]>(`/admin/webhooks/${id}/deliveries`),
  },
};
