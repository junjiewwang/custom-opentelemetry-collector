/**
 * View: Service Map / Topology
 */
ViewRouter.register('topology', {
  render: function(container) {
    container.innerHTML = '\
        <div class="flex items-center justify-between mb-lg">\
          <div class="flex items-center gap-md"><span class="badge badge-info">18 services</span><span class="badge badge-neutral">42 edges</span></div>\
          <div class="flex gap-sm"><button class="btn btn-ghost"><i class="fas fa-search-plus"></i></button><button class="btn btn-ghost"><i class="fas fa-search-minus"></i></button><button class="btn btn-ghost"><i class="fas fa-expand"></i></button></div>\
        </div>\
        <div class="topology-container">\
          <svg style="position:absolute;inset:0;width:100%;height:100%;pointer-events:none;z-index:1">\
            <line x1="180" y1="120" x2="340" y2="200" stroke="#30363d" stroke-width="1.5" stroke-dasharray="4,4"/>\
            <line x1="180" y1="120" x2="340" y2="80" stroke="#58a6ff" stroke-width="2" opacity="0.6"/>\
            <line x1="340" y1="200" x2="520" y2="160" stroke="#30363d" stroke-width="1.5"/>\
            <line x1="340" y1="80" x2="520" y2="160" stroke="#30363d" stroke-width="1.5"/>\
            <line x1="520" y1="160" x2="680" y2="100" stroke="#f85149" stroke-width="2" opacity="0.6"/>\
            <line x1="520" y1="160" x2="680" y2="240" stroke="#30363d" stroke-width="1.5"/>\
            <line x1="340" y1="200" x2="520" y2="320" stroke="#30363d" stroke-width="1.5"/>\
            <line x1="520" y1="320" x2="680" y2="360" stroke="#30363d" stroke-width="1.5"/>\
            <line x1="180" y1="120" x2="120" y2="280" stroke="#30363d" stroke-width="1.5"/>\
            <line x1="120" y1="280" x2="340" y2="340" stroke="#d29922" stroke-width="2" opacity="0.6"/>\
          </svg>\
          <div class="topo-node" style="left:140px;top:90px"><div class="topo-node-circle" style="border-color:var(--accent-blue);background:rgba(88,166,255,0.1)"><i class="fas fa-globe" style="color:var(--accent-blue)"></i></div><span class="topo-node-label">API Gateway</span></div>\
          <div class="topo-node" style="left:300px;top:50px"><div class="topo-node-circle" style="border-color:var(--accent-green);background:rgba(63,185,80,0.1)"><i class="fas fa-user" style="color:var(--accent-green)"></i></div><span class="topo-node-label">user-auth</span></div>\
          <div class="topo-node" style="left:300px;top:170px"><div class="topo-node-circle" style="border-color:var(--accent-green);background:rgba(63,185,80,0.1)"><i class="fas fa-shopping-cart" style="color:var(--accent-green)"></i></div><span class="topo-node-label">order-service</span></div>\
          <div class="topo-node" style="left:480px;top:130px"><div class="topo-node-circle" style="border-color:var(--accent-yellow);background:rgba(210,153,34,0.1)"><i class="fas fa-credit-card" style="color:var(--accent-yellow)"></i></div><span class="topo-node-label">payment-gateway</span></div>\
          <div class="topo-node" style="left:640px;top:70px"><div class="topo-node-circle" style="border-color:var(--accent-red);background:rgba(248,81,73,0.1)"><i class="fas fa-boxes" style="color:var(--accent-red)"></i></div><span class="topo-node-label">inventory-svc</span></div>\
          <div class="topo-node" style="left:640px;top:210px"><div class="topo-node-circle" style="border-color:var(--accent-green);background:rgba(63,185,80,0.1)"><i class="fas fa-bell" style="color:var(--accent-green)"></i></div><span class="topo-node-label">notification-svc</span></div>\
          <div class="topo-node" style="left:80px;top:250px"><div class="topo-node-circle" style="border-color:var(--accent-purple);background:rgba(188,140,255,0.1)"><i class="fas fa-database" style="color:var(--accent-purple)"></i></div><span class="topo-node-label">PostgreSQL</span></div>\
          <div class="topo-node" style="left:300px;top:310px"><div class="topo-node-circle" style="border-color:var(--accent-yellow);background:rgba(210,153,34,0.1)"><i class="fas fa-bolt" style="color:var(--accent-yellow)"></i></div><span class="topo-node-label">Redis Cache</span></div>\
          <div class="topo-node" style="left:480px;top:290px"><div class="topo-node-circle" style="border-color:var(--accent-green);background:rgba(63,185,80,0.1)"><i class="fas fa-envelope" style="color:var(--accent-green)"></i></div><span class="topo-node-label">Kafka</span></div>\
          <div class="topo-node" style="left:640px;top:330px"><div class="topo-node-circle" style="border-color:var(--accent-green);background:rgba(63,185,80,0.1)"><i class="fas fa-brain" style="color:var(--accent-green)"></i></div><span class="topo-node-label">recommendation</span></div>\
        </div>\
        <div class="flex items-center gap-lg" style="margin-top:var(--space-lg)">\
          <span class="text-xs text-tertiary">Legend:</span>\
          <span class="flex items-center gap-sm text-xs"><span class="dot dot-healthy"></span> Healthy</span>\
          <span class="flex items-center gap-sm text-xs"><span class="dot dot-warning"></span> Degraded</span>\
          <span class="flex items-center gap-sm text-xs"><span class="dot dot-critical"></span> Critical</span>\
          <span class="flex items-center gap-sm text-xs" style="margin-left:var(--space-xl)"><span style="width:20px;height:2px;background:var(--accent-blue);border-radius:1px"></span> High traffic</span>\
          <span class="flex items-center gap-sm text-xs"><span style="width:20px;height:2px;background:var(--accent-red);border-radius:1px"></span> Errors</span>\
        </div>';
  }
});
