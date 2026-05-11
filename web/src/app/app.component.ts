import { Component } from '@angular/core';
import { RouterLink, RouterLinkActive, RouterOutlet } from '@angular/router';

@Component({
  selector: 'app-root',
  standalone: true,
  imports: [RouterOutlet, RouterLink, RouterLinkActive],
  template: `
    <header class="topbar">
      <div class="brand">Relay CI</div>
      <nav>
        <a routerLink="/builds" routerLinkActive="active">Builds</a>
      </nav>
    </header>
    <main>
      <router-outlet />
    </main>
  `,
  styles: [`
    :host { display: block; min-height: 100vh; }
    .topbar {
      display: flex; align-items: center; gap: 24px;
      padding: 12px 24px;
      background: var(--panel);
      border-bottom: 1px solid var(--border);
    }
    .brand { font-weight: 600; font-size: 16px; letter-spacing: 0.02em; }
    nav a {
      color: var(--muted);
      padding: 6px 10px;
      border-radius: 6px;
    }
    nav a.active { color: var(--text); background: var(--panel-2); }
    main { padding: 24px; max-width: 1280px; margin: 0 auto; }
  `],
})
export class AppComponent {}
