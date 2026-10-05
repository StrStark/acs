import { Navigate, Route, Routes, useLocation } from "react-router";
import { useQuery } from "@tanstack/react-query";
import { getMe, getSetupStatus } from "./lib/api";
import { Layout } from "./components/Layout";
import { Alert, FullPageSpinner } from "./components/ui";
import { UploadProvider } from "./components/Uploads";
import { SetupPage } from "./pages/SetupPage";
import { LoginPage } from "./pages/LoginPage";
import { DashboardPage } from "./pages/DashboardPage";
import { BucketsPage } from "./pages/BucketsPage";
import { BucketPage } from "./pages/BucketPage";
import { SharesPage } from "./pages/SharesPage";
import { KeysPage } from "./pages/KeysPage";
import { UsersPage } from "./pages/UsersPage";
import { WebhooksPage } from "./pages/WebhooksPage";
import { AuditPage } from "./pages/AuditPage";
import { SettingsPage } from "./pages/SettingsPage";
import { PublicSharePage } from "./pages/PublicSharePage";

export function App() {
  const location = useLocation();
  // Public share links work without an account, so skip the auth checks.
  if (location.pathname.startsWith("/s/")) {
    return (
      <Routes>
        <Route path="/s/:token" element={<PublicSharePage />} />
      </Routes>
    );
  }
  return <AuthenticatedApp />;
}

function AuthenticatedApp() {
  const setup = useQuery({ queryKey: ["setup"], queryFn: getSetupStatus, staleTime: Infinity });
  const me = useQuery({ queryKey: ["me"], queryFn: getMe, staleTime: Infinity });

  if (setup.isPending || me.isPending) return <FullPageSpinner />;
  if (setup.error || me.error) {
    return (
      <div className="mx-auto max-w-md p-8">
        <Alert>Cannot reach the server: {(setup.error ?? me.error)!.message}</Alert>
      </div>
    );
  }

  const needsSetup = setup.data.needsSetup;
  const user = me.data;

  return (
    <Routes>
      <Route path="/setup" element={<SetupPage status={setup.data} />} />
      <Route
        path="/login"
        element={needsSetup ? <Navigate to="/setup" replace /> : user ? <Navigate to="/" replace /> : <LoginPage />}
      />
      <Route
        element={
          needsSetup ? (
            <Navigate to="/setup" replace />
          ) : user ? (
            <UploadProvider>
              <Layout user={user} />
            </UploadProvider>
          ) : (
            <Navigate to="/login" replace />
          )
        }
      >
        <Route index element={<DashboardPage />} />
        <Route path="buckets" element={<BucketsPage />} />
        <Route path="buckets/:bucket/*" element={<BucketPage />} />
        <Route path="shares" element={<SharesPage />} />
        <Route path="keys" element={<KeysPage />} />
        <Route path="users" element={<UsersPage />} />
        <Route path="webhooks" element={<WebhooksPage />} />
        <Route path="audit" element={<AuditPage />} />
        <Route path="settings" element={<SettingsPage />} />
        <Route path="*" element={<Navigate to="/" replace />} />
      </Route>
    </Routes>
  );
}
