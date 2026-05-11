import { ChangeDetectionStrategy, Component, Input, OnDestroy, OnInit, inject, signal } from '@angular/core';
import { CommonModule } from '@angular/common';
import { RouterLink } from '@angular/router';
import { Subscription, interval, of, startWith, switchMap } from 'rxjs';
import { catchError } from 'rxjs/operators';

import { ApiService, BuildDetail } from '../api.service';
import { humanDuration, shortSha, shortTime } from '../format';

@Component({
  selector: 'app-build-detail',
  standalone: true,
  imports: [CommonModule, RouterLink],
  changeDetection: ChangeDetectionStrategy.OnPush,
  template: `
    <p><a routerLink="/builds">← All builds</a></p>

    <ng-container *ngIf="error() as err; else content">
      <p class="error">Failed to load build: {{ err }}</p>
    </ng-container>

    <ng-template #content>
      <ng-container *ngIf="build() as b">
        <section class="panel">
          <div class="panel-header">
            <div>
              <span class="badge" [class]="b.state">{{ b.state }}</span>
              <span class="mono build-id">{{ b.id }}</span>
            </div>
            <span class="muted" *ngIf="lastUpdated()">updated {{ shortTime(lastUpdated()!) }}</span>
          </div>
          <div class="meta">
            <div><label>Repo</label><span>{{ b.repoFullName || '—' }}</span></div>
            <div><label>Branch</label><span>{{ b.branch || '—' }}</span></div>
            <div><label>Commit</label><span class="mono">{{ shortSha(b.commitSha) || '—' }}</span></div>
            <div><label>Triggered by</label><span>{{ b.triggeredBy || '—' }}</span></div>
            <div><label>Started</label><span>{{ shortTime(b.startedAt || b.createdAt) }}</span></div>
            <div><label>Duration</label><span>{{ humanDuration(b.startedAt, b.finishedAt) }}</span></div>
          </div>
        </section>

        <section class="panel" style="margin-top: 16px;">
          <div class="panel-header">
            <h2>Stages</h2>
            <span class="muted">
              {{ b.tasksPassed }} passed · {{ b.tasksFailed }} failed · {{ b.tasksRunning }} running · {{ b.taskCount }} total
            </span>
          </div>
          <table>
            <thead>
              <tr>
                <th>State</th>
                <th>Stage</th>
                <th>Depends on</th>
                <th>Duration</th>
                <th>Exit</th>
                <th>Error</th>
              </tr>
            </thead>
            <tbody>
              <tr *ngFor="let t of b.tasks; trackBy: trackTaskById">
                <td><span class="badge" [class]="t.state">{{ t.state }}</span></td>
                <td><span class="task-name">{{ t.name }}</span></td>
                <td class="mono muted">{{ formatDeps(t.dependsOn) }}</td>
                <td>{{ humanDuration(t.startedAt, t.finishedAt) }}</td>
                <td>{{ t.exitCode }}</td>
                <td class="error-cell">{{ t.error || '' }}</td>
              </tr>
            </tbody>
          </table>
        </section>
      </ng-container>
    </ng-template>
  `,
  styles: [`
    .panel {
      background: var(--panel);
      border: 1px solid var(--border);
      border-radius: 8px;
      overflow: hidden;
    }
    .panel-header {
      display: flex; justify-content: space-between; align-items: center;
      padding: 12px 16px;
      border-bottom: 1px solid var(--border);
    }
    h2 { margin: 0; font-size: 14px; font-weight: 600; }
    .build-id { margin-left: 12px; }
    .muted { color: var(--muted); font-size: 12px; }
    .error { color: var(--failed); }
    .meta {
      display: grid;
      grid-template-columns: repeat(auto-fit, minmax(180px, 1fr));
      gap: 12px;
      padding: 16px;
    }
    .meta label {
      display: block;
      font-size: 11px;
      color: var(--muted);
      text-transform: uppercase;
      letter-spacing: 0.04em;
      margin-bottom: 2px;
    }
    .task-name { font-weight: 500; }
    .error-cell { color: var(--failed); max-width: 360px; overflow: hidden; text-overflow: ellipsis; }
    tbody tr:hover { background: var(--panel-2); }
  `],
})
export class BuildDetailComponent implements OnInit, OnDestroy {
  private api = inject(ApiService);
  private sub?: Subscription;

  @Input({ required: true }) id!: string;

  readonly build = signal<BuildDetail | null>(null);
  readonly error = signal<string | null>(null);
  readonly lastUpdated = signal<string | null>(null);

  readonly shortSha = shortSha;
  readonly shortTime = shortTime;
  readonly humanDuration = humanDuration;

  ngOnInit(): void {
    this.sub = interval(2000)
      .pipe(
        startWith(0),
        switchMap(() =>
          this.api.getBuild(this.id).pipe(
            catchError((err) => {
              this.error.set(err?.message ?? 'unknown error');
              return of(null);
            }),
          ),
        ),
      )
      .subscribe((b) => {
        if (b) {
          this.build.set(b);
          this.error.set(null);
          this.lastUpdated.set(new Date().toISOString());
        }
      });
  }

  ngOnDestroy(): void {
    this.sub?.unsubscribe();
  }

  trackTaskById(_: number, t: { id: string }): string {
    return t.id;
  }

  formatDeps(deps: string[]): string {
    if (!deps || deps.length === 0) return '—';
    return deps.join(', ');
  }
}
