import { Routes } from '@angular/router';
import { BuildsListComponent } from './builds-list/builds-list.component';
import { BuildDetailComponent } from './build-detail/build-detail.component';

export const routes: Routes = [
  { path: '', pathMatch: 'full', redirectTo: 'builds' },
  { path: 'builds', component: BuildsListComponent },
  { path: 'builds/:id', component: BuildDetailComponent },
  { path: '**', redirectTo: 'builds' },
];
