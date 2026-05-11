import { ChangeDetectionStrategy, Component, OnDestroy, OnInit, inject, signal } from '@angular/core';
import { CommonModule } from '@angular/common';
import { RouterLink } from '@angular/router';
import { Subscription, interval, startWith, switchMap } from 'rxjs';

import { ApiService, BuildSummary } from '../api.service';
import { humanDuration, shortSha, shortTime } from '../format';

@Component({
  selector: 'app-builds-list',
  standalone: true,
  imports: [CommonModule, RouterLink],
  changeDetection: ChangeDetectionStrategy.OnPush,
  template: `
    <section class="panel">
      <div class="panel-header">
        <h2>Builds</h2>
        <span class="muted" *ngIf="lastUpdated()">updated {{ shortTime(lastUpdated()!) }}</span>
      </div>

      <ng-container *ngIf="error(); else loaded">
        <p class="error">Failed to load builds: {{ error() }}</p>
      </ng-container>

      <ng-template #loaded>
        <table *ngIf="builds().length > 0; else empty">
          <thead>
            <tr>
              <th>State</th>
              <th>Build</th>
              <th>Repo</th>
              <th>Branch</th>
              <th>Commit</th>
              <th>Tasks</th>
              <th>Duration</th>
              <th>Started</th>
            </tr>
          </thead>
          <tbody>
            <tr *ngFor="let b of builds(); trackBy: trackById">
              <td><span class="badge" [class]="b.state">{{ b.state }}</span></td>
              <td><a [routerLink]="['/builds', b.id]" class="mono">{{ b.id }}</a></td>
              <td>{{ b.repoFullName || '—' }}</td>
              <td>{{ b.branch || '—' }}</td>
              <td class="mono">{{ shortSha(b.commitSha) || '—' }}</td>
              <td>
                <span class="passed-count">{{ b.tasksPassed }}</span>
                /
                <span>{{ b.taskCount }}</span>
                <span *ngIf="b.tasksFailed > 0" class="failed-count"> · {{ b.tasksFailed }} failed</span>
              </td>
              <td>{{ humanDuration(b.startedAt, b.finishedAt) }}</td>
              <td>{{ shortTime(b.startedAt || b.createdAt) }}</td>
            </tr>
          </tbody>
        </table>

        <ng-template #empty>
          <p class="muted">No builds yet. Submit one via the CLI or webhook.</p>
        </ng-template>
      </ng-template>
    </section>
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
    .error { padding: 16px; color: var(--failed); }
    .muted { color: var(--muted); font-size: 12px; }
    .passed-count { color: var(--passed); }
    .failed-count { color: var(--failed); }
    tbody tr:hover { background: var(--panel-2); }
  `],
})
export class BuildsListComponent implements OnInit, OnDestroy {
  private api = inject(ApiService);
  private sub?: Subscription;

  readonly builds = signal<BuildSummary[]>([]);
  readonly error = signal<string | null>(null);
  readonly lastUpdated = signal<string | null>(null);

  readonly shortSha = shortSha;
  readonly shortTime = shortTime;
  readonly humanDuration = humanDuration;

  ngOnInit(): void {
    this.sub = interval(3000)
      .pipe(
        startWith(0),
        switchMap(() => this.api.listBuilds()),
      )
      .subscribe({
        next: (resp) => {
          this.builds.set(resp.builds ?? []);
          this.error.set(null);
          this.lastUpdated.set(new Date().toISOString());
        },
        error: (err) => this.error.set(err?.message ?? 'unknown error'),
      });
  }

  ngOnDestroy(): void {
    this.sub?.unsubscribe();
  }

  trackById(_: number, b: BuildSummary): string {
    return b.id;
  }
}
