import React from 'react';
import { createRoot } from 'react-dom/client';
import '../i18n';
import '../styles.css';
import App from './App';

const container = document.getElementById('root');
if (container) {
  createRoot(container).render(
    <React.StrictMode>
      <App />
    </React.StrictMode>,
  );
}
