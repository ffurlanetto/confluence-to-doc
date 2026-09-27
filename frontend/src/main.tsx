import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { StrictMode } from 'react';
import { createRoot } from 'react-dom/client';
import { BrowserRouter } from 'react-router-dom';

import { App } from './App';
import { isUnauthenticated } from './api/client';
import './styles.css';

const queryClient = new QueryClient({
  defaultOptions: {
    queries: {
      // Never retry auth errors; retry transient failures a couple of times.
      retry: (count, error) => !isUnauthenticated(error) && count < 2,
      refetchOnWindowFocus: true,
    },
  },
});

// A 401 on any query means the session expired: go back to the login screen.
queryClient.getQueryCache().subscribe((event) => {
  if (
    event.type === 'updated' &&
    isUnauthenticated(event.query.state.error) &&
    event.query.queryKey[0] !== 'me'
  ) {
    void queryClient.invalidateQueries({ queryKey: ['me'] });
  }
});

const root = document.getElementById('root');
if (!root) throw new Error('#root element missing');

createRoot(root).render(
  <StrictMode>
    <QueryClientProvider client={queryClient}>
      <BrowserRouter>
        <App />
      </BrowserRouter>
    </QueryClientProvider>
  </StrictMode>,
);
