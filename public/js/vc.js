lucide.createIcons();

// Global variables for dragging
let pos1 = 0, pos2 = 0, pos3 = 0, pos4 = 0;

// 1. Control Bar & Sidebar Logic
document.querySelectorAll('.icon-btn').forEach(btn => {
  btn.addEventListener('click', function() {
    const icon = this.querySelector('[data-lucide]');
    if (!icon) return;
    const currentIcon = icon.getAttribute('data-lucide');
    const isActive = this.classList.toggle('active');
    const pairs = { 'mic': 'mic-off', 'mic-off': 'mic', 'video': 'video-off', 'video-off': 'video', 'hand': 'hand-metal', 'hand-metal': 'hand', 'monitor': 'x-circle', 'x-circle': 'monitor' };

    if (pairs[currentIcon]) {
      icon.setAttribute('data-lucide', pairs[currentIcon]);
      lucide.createIcons();
      if (currentIcon.includes('mic')) {
        this.style.background = isActive ? '#565152' : '#2a2a2a';
        document.querySelectorAll('.video-tile').forEach(t => t.classList.toggle('is-muted', isActive));
      } else {
        this.style.background = isActive ? '#565152' : '#2a2a2a';
      }
    }
  });
});

function toggleSidebar() {
  const sidebar = document.getElementById('sidebar');
  window.innerWidth > 768 ? sidebar.classList.toggle('hidden') : sidebar.classList.toggle('open');
}

function switchTab(tab, btnElement) {
  const clickedBtn = btnElement || event.currentTarget;
  document.querySelectorAll('.tab-btn').forEach(b => b.classList.remove('active'));
  if (clickedBtn) clickedBtn.classList.add('active');
  document.querySelectorAll('.sidebar-content').forEach(c => c.classList.remove('active'));
  const targetSection = document.getElementById(`${tab}-section`);
  if (targetSection) targetSection.classList.add('active');
  
  // RESTORED: Calls the people list generator when tab is switched
  if (tab === 'people') updatePeopleList();
}

// 2. Dynamic Pinning
document.querySelector('.video-grid').addEventListener('click', function(e) {
  const clickedTile = e.target.closest('.video-tile');
  if (clickedTile && clickedTile.id !== 'tile-susan') {
    const grid = document.querySelector('.video-grid');
    const currentHost = document.getElementById('tile-susan');

    if (currentHost) {
      currentHost.classList.remove('is-minimized');
      currentHost.style.top = ""; currentHost.style.left = "";
      currentHost.style.bottom = ""; currentHost.style.right = "";
      currentHost.onmousedown = null; 
      const icon = currentHost.querySelector('.minimize-btn [data-lucide]');
      if(icon) icon.setAttribute('data-lucide', 'minimize-2');
    }

    grid.scrollTo({ top: 0, behavior: 'smooth' });
    setTimeout(() => {
      if (currentHost) currentHost.id = '';
      clickedTile.id = 'tile-susan';
      
      // Update people list if the tab is currently open
      if (document.getElementById('people-section').classList.contains('active')) {
        updatePeopleList();
      }
      lucide.createIcons();
    }, 100); 
  }
});

// 3. RESTORED: People List Generator
function updatePeopleList() {
  const peopleContainer = document.querySelector('#people-section .people-list');
  const tiles = document.querySelectorAll('.video-tile');
  if (!peopleContainer) return;

  peopleContainer.innerHTML = ''; // Clear current list

  tiles.forEach(tile => {
    const name = tile.querySelector('.tile-overlay span').innerText;
    const isMuted = tile.classList.contains('is-muted');
    const personItem = document.createElement('div');
    personItem.className = 'person-item';
    personItem.innerHTML = `
      <div class="person-info">
        <i data-lucide="user" class="user-icon"></i>
        <span>${name}</span>
      </div>
      <i data-lucide="${isMuted ? 'mic-off' : 'mic'}" class="${isMuted ? 'muted-text' : ''}"></i>
    `;
    peopleContainer.appendChild(personItem);
  });
  lucide.createIcons(); // Refresh icons for the list
}

// 4. Minimize & Draggable Logic
function toggleMinimize(e) {
  e.stopPropagation();
  const pinnedTile = document.getElementById('tile-susan');
  if (!pinnedTile) return;

  const isMinimized = pinnedTile.classList.toggle('is-minimized');
  const icon = e.currentTarget.querySelector('[data-lucide]');
  
  if (isMinimized) {
    icon.setAttribute('data-lucide', 'maximize-2');
    startDraggable(pinnedTile);
  } else {
    icon.setAttribute('data-lucide', 'minimize-2');
    pinnedTile.onmousedown = null;
    pinnedTile.style.top = ""; pinnedTile.style.left = "";
    pinnedTile.style.bottom = ""; pinnedTile.style.right = "";
  }
  lucide.createIcons();
}

function startDraggable(elmnt) {
  if (window.innerWidth <= 768) return;

  elmnt.onmousedown = (e) => {
    if (e.target.closest('.minimize-btn')) return;
    e.preventDefault();
    pos3 = e.clientX;
    pos4 = e.clientY;
    document.onmouseup = () => {
      document.onmouseup = null;
      document.onmousemove = null;
    };
    document.onmousemove = (e) => {
      pos1 = pos3 - e.clientX;
      pos2 = pos4 - e.clientY;
      pos3 = e.clientX;
      pos4 = e.clientY;
      elmnt.style.top = (elmnt.offsetTop - pos2) + "px";
      elmnt.style.left = (elmnt.offsetLeft - pos1) + "px";
      elmnt.style.bottom = "auto"; 
      elmnt.style.right = "auto";
    };
  };
}
