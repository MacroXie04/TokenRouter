import { useEffect, useState } from 'react';

function currentLocation() {
  return { pathname: window.location.pathname, search: window.location.search };
}

export function navigate(to: string, replace = false) {
  if (replace) window.history.replaceState(null, '', to);
  else window.history.pushState(null, '', to);
  window.dispatchEvent(new PopStateEvent('popstate'));
  window.scrollTo?.({ top: 0, behavior: 'instant' });
}

export function useBrowserLocation() {
  const [location, setLocation] = useState(currentLocation);
  useEffect(() => {
    const update = () => setLocation(currentLocation());
    window.addEventListener('popstate', update);
    return () => window.removeEventListener('popstate', update);
  }, []);
  return location;
}
