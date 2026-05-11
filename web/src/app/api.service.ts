import { Injectable, inject } from '@angular/core';
import { HttpClient } from '@angular/common/http';
import { Observable } from 'rxjs';

export type BuildState = 'queued' | 'running' | 'passed' | 'failed';

export type TaskState =
  | 'pending'
  | 'ready'
  | 'scheduled'
  | 'running'
  | 'passed'
  | 'failed'
  | 'skipped'
  | 'cancelled'
  | 'timed_out';

export interface BuildSummary {
  id: string;
  state: BuildState;
  repoFullName?: string;
  repoUrl?: string;
  branch?: string;
  commitSha?: string;
  prNumber?: string;
  triggeredBy?: string;
  createdAt: string;
  startedAt?: string;
  finishedAt?: string;
  taskCount: number;
  tasksPassed: number;
  tasksFailed: number;
  tasksRunning: number;
}

export interface TaskView {
  id: string;
  name: string;
  state: TaskState;
  exitCode: number;
  error?: string;
  dependsOn: string[];
  startedAt?: string;
  finishedAt?: string;
}

export interface BuildDetail extends BuildSummary {
  tasks: TaskView[];
}

export interface WorkerSummary {
  id: string;
  state: 'active' | 'draining' | 'dead';
  runningTasks: number;
  maxTasks: number;
  lastHeartbeat?: string;
  labels?: Record<string, string>;
}

@Injectable({ providedIn: 'root' })
export class ApiService {
  private http = inject(HttpClient);

  listBuilds(): Observable<{ builds: BuildSummary[] }> {
    return this.http.get<{ builds: BuildSummary[] }>('/api/v1/builds');
  }

  getBuild(id: string): Observable<BuildDetail> {
    return this.http.get<BuildDetail>(`/api/v1/builds/${encodeURIComponent(id)}`);
  }

  listWorkers(): Observable<{ workers: WorkerSummary[] }> {
    return this.http.get<{ workers: WorkerSummary[] }>('/api/v1/workers');
  }
}
