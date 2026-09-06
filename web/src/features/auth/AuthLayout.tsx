import { useId, type ReactNode } from 'react';
import { SystemBrand, useSystemBrand } from '../../shared/ui/SystemBrand';

export function AuthLayout({
  title,
  description,
  children,
}: {
  title: ReactNode;
  description?: ReactNode;
  children: ReactNode;
}) {
  const titleId = useId();
  const { loading } = useSystemBrand();

  return (
    <main
      className="app auth-layout"
      data-auth-layout="true"
      aria-labelledby={titleId}
      aria-busy={loading || undefined}
    >
      <SystemBrand variant="auth" />
      <div className="auth-layout-content">
        <header className="header">
          <h1 id={titleId}>{title}</h1>
          {description && <p className="tagline">{description}</p>}
        </header>
        {children}
      </div>
    </main>
  );
}
