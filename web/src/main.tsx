import React from 'react';
import ReactDOM from 'react-dom/client';
import { BrowserRouter } from 'react-router-dom';
import { App } from './App';
import { AuthProvider } from './AuthContext';
import { ThemeProvider } from './ThemeContext';
import './styles/globals.css';
import './design';
import './i18n';
import { registerServiceWorker } from './pwa';

// 注册 PWA Service Worker（仅生产构建；见 src/pwa.ts）。
void registerServiceWorker();

ReactDOM.createRoot(document.getElementById('root')!).render(
  <React.StrictMode>
    <BrowserRouter>
      <AuthProvider>
        <ThemeProvider>
          <App />
        </ThemeProvider>
      </AuthProvider>
    </BrowserRouter>
  </React.StrictMode>,
);
