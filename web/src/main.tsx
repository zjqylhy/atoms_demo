import React from 'react';
import {createRoot} from 'react-dom/client';
import App from './App';
import {SharedView} from './components/SharedView';
import './style.css';
const share=location.pathname.match(/^\/share\/([a-f0-9]{64})$/);
createRoot(document.getElementById('root')!).render(<React.StrictMode>{share?<SharedView token={share[1]}/>:<App/>}</React.StrictMode>);
