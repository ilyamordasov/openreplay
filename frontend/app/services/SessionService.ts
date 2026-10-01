import APIClient, { clean as cleanParams } from 'App/api_client';
import { ISession } from 'Types/session/session';
import { IErrorStack } from 'Types/session/errorStack';
import { unzipSync } from 'fflate';

export default class SettingsService {
  private client: APIClient;

  constructor(client?: APIClient) {
    this.client = client || new APIClient();
  }

  initClient(client?: APIClient) {
    this.client = client || new APIClient();
  }

  saveCaptureRate(projectId: number, data: any) {
    return this.client.post(`/${projectId}/sample_rate`, data);
  }

  fetchCaptureRate(projectId: number) {
    return this.client
      .get(`/${projectId}/sample_rate`)
      .then((response) => response.json())
      .then((response) => response.data || 0);
  }

  fetchCaptureConditions(
    projectId: number,
  ): Promise<{ rate: number; conditionalCapture: boolean; conditions: any[] }> {
    return this.client
      .get(`/${projectId}/conditions`)
      .then((response) => response.json())
      .then((response) => response.data || []);
  }

  saveCaptureConditions(projectId: number, data: any) {
    return this.client.post(`/${projectId}/conditions`, data);
  }

  getSessions(filter: any): Promise<{
    sessions: ISession[];
    groups?: Array<{
      groupId: string;
      startTs: number;
      endTs: number;
      eventsCount: number;
      sessions: ISession[];
    }>;
    total: number;
  }> {
    return this.client
      .post('/sessions/search', filter)
      .then((r) => r.json())
      .then((response) => response.data || [])
      .catch((e) => Promise.reject(e));
  }

  getFirstMobUrl(
    sessionId: string,
  ): Promise<{ domURL: string[]; fileKey?: string }> {
    return this.client
      .get(`/sessions/${sessionId}/first-mob`)
      .then((r) => r.json())
      .then((j) => j.data || {})
      .catch(console.error);
  }

  async downloadSession(sessionId: string): Promise<void> {
    const response = await this.client.get(`/sessions/${sessionId}/download`);
    const blob = await response.blob();
    const contentDisposition = response.headers.get('Content-Disposition');
    const filenameMatch = contentDisposition?.match(/filename="?([^";]+)"?/i);
    const filename =
      filenameMatch?.[1] || `openreplay-session-${sessionId}.zip`;
    const url = URL.createObjectURL(blob);
    const anchor = document.createElement('a');
    anchor.href = url;
    anchor.download = filename;
    document.body.appendChild(anchor);
    anchor.click();
    anchor.remove();
    URL.revokeObjectURL(url);
  }

  async downloadSessionGroup(sessionIds: string[]): Promise<void> {
    if (sessionIds.length === 0) {
      throw new Error('No sessions to export');
    }
    const response = await this.client.post('/sessions/download', {
      sessionIds,
    });
    if (!response.ok) {
      let message = 'Failed to export sessions';
      try {
        const payload = await response.json();
        message =
          payload?.errors?.[0] ||
          payload?.error ||
          payload?.message ||
          message;
      } catch {
        // Keep the generic message when the API response is not JSON.
      }
      throw new Error(message);
    }

    const blob = await response.blob();
    const contentDisposition = response.headers.get('Content-Disposition');
    const filenameMatch = contentDisposition?.match(/filename="?([^";]+)"?/i);
    const filename =
      filenameMatch?.[1] ||
      `openreplay-stitched-${sessionIds[0]}-${sessionIds.length}.zip`;
    const url = URL.createObjectURL(blob);
    const anchor = document.createElement('a');
    anchor.href = url;
    anchor.download = filename;
    document.body.appendChild(anchor);
    anchor.click();
    anchor.remove();
    URL.revokeObjectURL(url);
  }


  async prepareStitchedSession(sessionIds: string[]): Promise<{
    manifest: {
      format: string;
      version: number;
      sessionId: string;
      sourceSessionIds: string[];
      files: string[];
      startTs: number;
      endTs: number;
      durationMs: number;
      gaps: string;
      segments: Array<{
        sessionId: string;
        sourceStartTs: number;
        sourceEndTs: number;
        targetStartTs: number;
        targetEndTs: number;
        durationMs: number;
      }>;
    };
    domURL: string[];
    devtoolsURL: string[];
    blobURLs: string[];
  }> {
    if (sessionIds.length === 0) {
      throw new Error('No sessions to play');
    }

    const response = await this.client.post('/sessions/download', {
      sessionIds,
    });
    if (!response.ok) {
      let message = 'Failed to prepare merged session';
      try {
        const payload = await response.json();
        message =
          payload?.errors?.[0] ||
          payload?.error ||
          payload?.message ||
          message;
      } catch {
        // Keep generic error for non-JSON API responses.
      }
      throw new Error(message);
    }

    const archive = unzipSync(new Uint8Array(await response.arrayBuffer()));
    const manifestBytes = archive['manifest.json'];
    const domBytes = archive['raw/dom.mobs'];
    if (!manifestBytes || !domBytes) {
      throw new Error('Merged session archive is incomplete');
    }

    const manifest = JSON.parse(
      new TextDecoder().decode(manifestBytes),
    ) as {
      format: string;
      version: number;
      sessionId: string;
      sourceSessionIds: string[];
      files: string[];
      startTs: number;
      endTs: number;
      durationMs: number;
      gaps: string;
      segments: Array<{
        sessionId: string;
        sourceStartTs: number;
        sourceEndTs: number;
        targetStartTs: number;
        targetEndTs: number;
        durationMs: number;
      }>;
    };
    const sourceIdsMatch =
      Array.isArray(manifest.sourceSessionIds) &&
      manifest.sourceSessionIds.length === sessionIds.length &&
      manifest.sourceSessionIds.every(
        (sessionId, index) => sessionId === sessionIds[index],
      );
    const segmentsMatch =
      Array.isArray(manifest.segments) &&
      manifest.segments.length === sessionIds.length &&
      manifest.segments.every((segment, index) => {
        const sourceStart = Number(segment.sourceStartTs);
        const sourceEnd = Number(segment.sourceEndTs);
        const targetStart = Number(segment.targetStartTs);
        const targetEnd = Number(segment.targetEndTs);
        const duration = Number(segment.durationMs);
        const previous = index > 0 ? manifest.segments[index - 1] : undefined;

        return (
          segment.sessionId === sessionIds[index] &&
          Number.isFinite(sourceStart) &&
          Number.isFinite(sourceEnd) &&
          Number.isFinite(targetStart) &&
          Number.isFinite(targetEnd) &&
          Number.isFinite(duration) &&
          sourceEnd >= sourceStart &&
          targetEnd >= targetStart &&
          duration === targetEnd - targetStart &&
          (!previous || targetStart > Number(previous.targetEndTs))
        );
      });
    const timelineMatches =
      Number.isFinite(Number(manifest.startTs)) &&
      Number.isFinite(Number(manifest.endTs)) &&
      Number.isFinite(Number(manifest.durationMs)) &&
      manifest.endTs >= manifest.startTs &&
      manifest.durationMs === manifest.endTs - manifest.startTs &&
      manifest.segments[0]?.targetStartTs === manifest.startTs &&
      manifest.segments[manifest.segments.length - 1]?.targetEndTs ===
        manifest.endTs;

    if (
      manifest.format !== 'openreplay-stitched-session-export' ||
      manifest.version !== 1 ||
      !sourceIdsMatch ||
      !segmentsMatch ||
      !timelineMatches
    ) {
      throw new Error('Merged session manifest is invalid');
    }

    const blobURLs: string[] = [];
    const makeURL = (bytes: Uint8Array) => {
      const url = URL.createObjectURL(
        new Blob([bytes], { type: 'application/octet-stream' }),
      );
      blobURLs.push(url);
      return url;
    };

    const domURL = [makeURL(domBytes)];
    const devtoolsBytes = archive['raw/devtools.mob'];
    const devtoolsURL = devtoolsBytes ? [makeURL(devtoolsBytes)] : [];

    return {
      manifest,
      domURL,
      devtoolsURL,
      blobURLs,
    };
  }

  getRecommendedSessions(sort?: any): Promise<{
    sessions: ISession[];
    total: number;
  }> {
    return this.client
      .post('/sessions-recommendations', sort)
      .then((r) => r.json())
      .then((response) => response || [])
      .catch((e) => Promise.reject(e));
  }

  getFinetuneSessions(): Promise<{ sessions: string[] }> {
    return this.client
      .get('/PROJECT_ID/finetuning/sessions')
      .then((r) => r.json())
      .catch(Promise.reject);
  }

  sendFeedback(data: any): Promise<any> {
    return this.client
      .post(`/session-feedback`, data)
      .then((r) => r.json())
      .then((j) => j.data || [])
      .catch(Promise.reject);
  }

  signalFinetune() {
    return this.client.get('/PROJECT_ID/finetune');
  }

  checkFeedback(sessionId: string): Promise<any> {
    return this.client
      .get(`/session-feedback/${sessionId}`)
      .then((r) => r.json())
      .then((j) => j.data || false)
      .catch(Promise.reject);
  }

  getSessionInfo(
    sessionId: string,
    isLive?: boolean,
    abortSignal?: AbortSignal,
  ): Promise<ISession> {
    return this.client
      .get(
        isLive
          ? `/assist/sessions/${sessionId}`
          : `/sessions/${sessionId}/replay`,
        undefined,
        undefined,
        undefined,
        abortSignal,
      )
      .then((r) => r.json())
      .then((j) => j.data || {})
      .catch(console.error);
  }

  getSessionEvents = async (sessionId: string) =>
    this.client
      .get(`/sessions/${sessionId}/events`)
      .then((r) => r.json())
      .then((j) => j.data || [])
      .catch(console.error);

  getLiveSessions(filter: any): Promise<{ sessions: ISession[] }> {
    return this.client
      .post('/assist/sessions', cleanParams(filter))
      .then((r) => r.json())
      .then((response) => response.data || [])
      .catch((e) => Promise.reject(e));
  }

  getErrorStack(
    sessionId: string,
    errorId: string,
  ): Promise<{ trace: IErrorStack[] }> {
    return this.client
      .get(`/sessions/${sessionId}/errors/${errorId}/sourcemaps`)
      .then((r) => r.json())
      .then((j) => j.data || {})
      .catch((e) => Promise.reject(e));
  }

  getAutoplayList(params = {}): Promise<{ sessionId: string }[]> {
    return this.client
      .post('/sessions/search/ids', cleanParams(params))
      .then((r) => r.json())
      .then((j) => j.data || [])
      .catch((e) => Promise.reject(e));
  }

  toggleFavorite(sessionId: string): Promise<any> {
    return this.client
      .get(`/sessions/${sessionId}/favorite`)
      .catch(Promise.reject);
  }

  getClickMap(params = {}): Promise<any[]> {
    return this.client
      .post('/heatmaps/url', params)
      .then((r) => r.json())
      .then((j) => j.data || [])
      .catch(Promise.reject);
  }

  getSessionClickMap(sessionId: string, params = {}): Promise<any[]> {
    return this.client
      .post(`/sessions/${sessionId}/clickmaps`, params)
      .then((r) => r.json())
      .then((j) => j.data || [])
      .catch(Promise.reject);
  }

  getRecordingStatus(): Promise<any> {
    return this.client
      .get('/check-recording-status')
      .then((r) => r.json())
      .then((j) => j.data || {})
      .catch(Promise.reject);
  }

  async fetchSimilarSessions(
    sessionId: string,
    params: any,
  ): Promise<{ sessions: ISession[] }> {
    try {
      const r = await this.client.post(
        `/PROJECT_ID/similar-sessions/${sessionId}`,
        params,
      );
      const j = await r.json();
      return j.sessions || [];
    } catch (reason) {
      return Promise.reject(reason);
    }
  }

  async getAssistCredentials(): Promise<any> {
    try {
      const r = await this.client.get('/config/assist/credentials');
      const j = await r.json();
      return j.data || null;
    } catch (reason) {
      return Promise.reject(reason);
    }
  }

  generateShorts(projectId: string) {
    try {
      void this.client.post(`/${projectId}/generate/shorts`, {});
    } catch (reason) {
      console.error('Error generating shorts:', reason);
    }
  }

  async fetchSessionClips(): Promise<{ clips: any[] }> {
    try {
      const r = await this.client.get('/PROJECT_ID/shorts-recommendations', {
        sortBy: 'startTs',
        sortOrder: 'desc',
      });
      const j = await r.json();
      return j || {};
    } catch (reason) {
      return Promise.reject(reason);
    }
  }
}
