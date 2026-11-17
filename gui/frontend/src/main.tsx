import { StrictMode } from "react";
import { createRoot } from "react-dom/client";
import "./index.css";
import { AppProvider } from "@/state/app";
import { UpdateProvider } from "@/state/update";
import { App } from "./App";

createRoot(document.getElementById("root")!).render(
  <StrictMode>
    <AppProvider>
      <UpdateProvider>
        <App />
      </UpdateProvider>
    </AppProvider>
  </StrictMode>,
);
