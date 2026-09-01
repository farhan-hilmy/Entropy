/**
 * @license
 * SPDX-License-Identifier: Apache-2.0
 */

import { useState, useEffect } from 'react';
import axios from 'axios';

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
    <div className="min-h-screen bg-neutral-50 text-neutral-900 p-8">
      <header className="mb-8">
        <h1 className="text-3xl font-bold">Entropy</h1>
        <p className="text-neutral-600">Manage your network failures</p>
      </header>

      <section className="mb-8 p-4 bg-white rounded-lg shadow-sm border border-neutral-200">
        <h2 className="text-xl font-semibold mb-4">Create Proxy</h2>
        <form onSubmit={createProxy} className="grid grid-cols-1 md:grid-cols-4 gap-4">
          <input name="name" placeholder="Proxy Name" className="p-2 border rounded" required />
          <input name="listen" placeholder="Listen (e.g. 0.0.0.0:8666)" className="p-2 border rounded" required />
          <input name="upstream" placeholder="Upstream (e.g. service:8080)" className="p-2 border rounded" required />
          <button type="submit" className="p-2 bg-neutral-900 text-white rounded">Create Proxy</button>
        </form>
      </section>

      <section>
        <h2 className="text-xl font-semibold mb-4">Proxies</h2>
        {loading && <p>Loading...</p>}
        {error && <p className="text-red-600">{error}</p>}
        {!loading && !error && proxies.length === 0 && <p className="text-neutral-600">No proxies defined.</p>}
        <div className="grid gap-4">
          {proxies.map(proxy => (
            <div key={proxy.name} className="p-4 bg-white rounded-lg shadow-sm border border-neutral-200">
              <h3 className="font-bold">{proxy.name}</h3>
              <p className="text-sm text-neutral-600">Listen: {proxy.listen}</p>
              <p className="text-sm text-neutral-600">Upstream: {proxy.upstream}</p>
            </div>
          ))}
        </div>
      </section>
    </div>
  );
}
