/**
 * @license
 * SPDX-License-Identifier: Apache-2.0
 */

import { useState, useEffect } from 'react';
import axios from 'axios';
import { motion } from 'motion/react';

interface Proxy {
  name: string;
  listen: string;
  upstream: string;
  enabled: boolean;
  toxics: any[];
}

export default function App() {
  const [proxies, setProxies] = useState<Proxy[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);

  useEffect(() => {
    fetchProxies();
  }, []);

  const fetchProxies = async () => {
    setLoading(true);
    setError(null);
    try {
      const response = await axios.get('/api/proxies');
      setProxies(Object.values(response.data));
    } catch (error: any) {
      console.error('Failed to fetch proxies', error);
      const errorMessage = error.response?.data?.error || 'Failed to fetch proxies. Please ensure Toxiproxy is running.';
      setError(errorMessage);
    } finally {
      setLoading(false);
    }
  };

  const createProxy = async (e: React.FormEvent<HTMLFormElement>) => {
    e.preventDefault();
    const formData = new FormData(e.currentTarget);
    const data = {
      name: formData.get('name'),
      listen: formData.get('listen'),
      upstream: formData.get('upstream'),
    };
    try {
      await axios.post('/api/proxies', data);
      fetchProxies();
      e.currentTarget.reset();
    } catch (error) {
      console.error('Failed to create proxy', error);
      alert('Failed to create proxy');
    }
  };

  return (
    <div className="min-h-screen bg-slate-950 text-slate-100 p-8">
      <header className="mb-8 border-b border-slate-800 pb-6">
        <h1 className="text-4xl font-extrabold tracking-tighter text-white">Entropy</h1>
        <p className="text-slate-400 mt-2 font-mono text-sm uppercase tracking-widest">Chaos Injection Engine</p>
      </header>

      <section className="mb-12 p-6 bg-slate-900 rounded border border-slate-800">
        <h2 className="text-xl font-semibold mb-6 text-white">Create Proxy</h2>
        <form onSubmit={createProxy} className="grid grid-cols-1 md:grid-cols-4 gap-4">
          <input name="name" placeholder="Proxy Name" className="p-3 bg-slate-800 border border-slate-700 rounded text-white focus:outline-none focus:border-indigo-500" required />
          <input name="listen" placeholder="Listen (e.g. 0.0.0.0:8666)" className="p-3 bg-slate-800 border border-slate-700 rounded text-white focus:outline-none focus:border-indigo-500" required />
          <input name="upstream" placeholder="Upstream (e.g. service:8080)" className="p-3 bg-slate-800 border border-slate-700 rounded text-white focus:outline-none focus:border-indigo-500" required />
          <button type="submit" className="p-3 bg-white text-slate-950 font-bold rounded hover:bg-slate-200 transition-colors">Create Proxy</button>
        </form>
      </section>

      <section>
        <h2 className="text-xl font-semibold mb-6 text-white">Proxies</h2>
        {loading && <p className="text-slate-400 font-mono">Initializing connection...</p>}
        {error && <p className="text-red-400 font-mono">{error}</p>}
        {!loading && !error && proxies.length === 0 && <p className="text-slate-500 font-mono italic">No proxies defined.</p>}
        <div className="grid grid-cols-1 md:grid-cols-2 lg:grid-cols-3 gap-6">
          {proxies.map(proxy => (
            <motion.div key={proxy.name} initial={{ opacity: 0, y: 10 }} animate={{ opacity: 1, y: 0 }} className="p-6 bg-slate-900 rounded border border-slate-800 hover:border-slate-600 transition-colors">
              <h3 className="font-bold text-lg text-white mb-2">{proxy.name}</h3>
              <div className="text-sm font-mono text-slate-400">
                <p>Listen: {proxy.listen}</p>
                <p>Upstream: {proxy.upstream}</p>
              </div>
            </motion.div>
          ))}
        </div>
      </section>
    </div>
  );
}
