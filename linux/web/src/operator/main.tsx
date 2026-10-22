// Sentinel Operator entry (operator.html): the desktop app's UI, also usable
// in a browser for development.
import { StrictMode } from "react";
import { createRoot } from "react-dom/client";
import App, { parseRoute } from "./App";
import "../styles.css";
import "../admin/admin.css";
import "../shared/look.css";
import "./operator.css";

createRoot(document.getElementById("root")!).render(
  <StrictMode>
    <App route={parseRoute(window.location.hash)} />
  </StrictMode>,
);
