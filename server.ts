import express from "express";
import path from "path";
import { createServer as createViteServer } from "vite";
import axios from "axios";

async function startServer() {
  const app = express();
  const PORT = 3000;
  const TOXIPROXY_URL = process.env.TOXIPROXY_URL || "http://localhost:8474";

  app.use(express.json());

  // API routes
  app.get("/api/health", (req, res) => {
    res.json({ status: "ok" });
  });

  app.get("/api/proxies", async (req, res) => {
    try {
      const response = await axios.get(`${TOXIPROXY_URL}/proxies`);
      res.json(response.data);
    } catch (error: any) {
      console.error("Error fetching proxies from Toxiproxy:", error.message);
      res.status(500).json({ error: "Could not connect to Toxiproxy. Ensure it is running at " + TOXIPROXY_URL });
    }
  });

  app.post("/api/proxies", async (req, res) => {
    try {
      const response = await axios.post(`${TOXIPROXY_URL}/proxies`, req.body);
      res.json(response.data);
    } catch (error: any) {
      console.error("Error creating proxy in Toxiproxy:", error.message);
      res.status(500).json({ error: "Could not connect to Toxiproxy to create proxy." });
    }
  });

  // Vite middleware for development
  if (process.env.NODE_ENV !== "production") {
    const vite = await createViteServer({
      server: { middlewareMode: true },
      appType: "spa",
    });
    app.use(vite.middlewares);
  } else {
    const distPath = path.join(process.cwd(), 'dist');
    app.use(express.static(distPath));
    app.get('*', (req, res) => {
      res.sendFile(path.join(distPath, 'index.html'));
    });
  }

  app.listen(PORT, "0.0.0.0", () => {
    console.log(`Server running on http://localhost:${PORT}`);
  });
}

startServer();
